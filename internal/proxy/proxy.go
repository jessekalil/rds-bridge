package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jessekalil/rds-bridge/internal/awsiam"
	"github.com/jessekalil/rds-bridge/internal/config"
)

// Proxy is a Postgres wire-protocol proxy for one RDS instance. The app
// authenticates with a static credential and picks the database in its startup
// message; each backend connection to RDS is opened with a freshly minted IAM
// token against that database (the token is database-agnostic).
type Proxy struct {
	listenPort   int
	local        config.Local
	ssmLocalPort int
	auth         *awsiam.Authenticator
	tls          *tls.Config
	logf         func(format string, args ...any)
}

// New builds a Proxy. tlsCfg (a self-signed cert generated once by SelfSignedTLS)
// lets the app connect with SSL without any cert files on disk.
func New(listenPort int, local config.Local, ssmLocalPort int, auth *awsiam.Authenticator, tlsCfg *tls.Config, logf func(format string, args ...any)) *Proxy {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Proxy{
		listenPort:   listenPort,
		local:        local,
		ssmLocalPort: ssmLocalPort,
		auth:         auth,
		tls:          tlsCfg,
		logf:         logf,
	}
}

// Listen serves until ctx is cancelled.
func (p *Proxy) Listen(ctx context.Context) error {
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(p.listenPort))
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	p.logf("proxy listening on %s", addr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go p.handle(ctx, conn)
	}
}

func (p *Proxy) handle(ctx context.Context, client net.Conn) {
	defer client.Close()
	remote := client.RemoteAddr()

	if err := p.serve(ctx, client); err != nil && !isClosed(err) {
		p.logf("connection %s closed: %v", remote, err)
		return
	}
	p.logf("connection %s closed", remote)
}

func (p *Proxy) serve(ctx context.Context, client net.Conn) error {
	be := pgproto3.NewBackend(client, client)

	startup, client, be, err := p.handleStartup(client, be)
	if err != nil {
		return err
	}

	sm, ok := startup.(*pgproto3.StartupMessage)
	if !ok {
		return fmt.Errorf("unexpected startup message %T", startup)
	}

	if err := p.authClient(sm, be, client); err != nil {
		writeFatal(be, "28P01", err.Error())
		return err
	}

	dbName := sm.Parameters["database"]
	if dbName == "" {
		writeFatal(be, "3D000", "no database specified")
		return errors.New("no database specified")
	}

	hj, err := p.dialBackend(ctx, dbName)
	if err != nil {
		writeFatal(be, "08006", fmt.Sprintf("backend connection to database %q failed", dbName))
		return fmt.Errorf("dial backend %q: %w", dbName, err)
	}
	defer hj.Conn.Close()
	p.logf("connection %s -> database %s", client.RemoteAddr(), dbName)

	if err := completeFrontendHandshake(be, hj); err != nil {
		return err
	}

	return pipe(client, hj.Conn)
}

// handleStartup reads the first message, upgrading to TLS if the client sends an
// SSLRequest. Returns the eventual StartupMessage and (possibly TLS-wrapped) conn.
func (p *Proxy) handleStartup(client net.Conn, be *pgproto3.Backend) (pgproto3.FrontendMessage, net.Conn, *pgproto3.Backend, error) {
	msg, err := be.ReceiveStartupMessage()
	if err != nil {
		return nil, client, be, fmt.Errorf("receive startup: %w", err)
	}

	switch msg.(type) {
	case *pgproto3.SSLRequest:
		if _, err := client.Write([]byte{'S'}); err != nil {
			return nil, client, be, err
		}
		tlsConn := tls.Server(client, p.tls)
		if err := tlsConn.Handshake(); err != nil {
			return nil, client, be, fmt.Errorf("tls handshake: %w", err)
		}
		be = pgproto3.NewBackend(tlsConn, tlsConn)
		msg, err = be.ReceiveStartupMessage()
		if err != nil {
			return nil, tlsConn, be, fmt.Errorf("receive startup after tls: %w", err)
		}
		return msg, tlsConn, be, nil
	default:
		return msg, client, be, nil
	}
}

func (p *Proxy) authClient(sm *pgproto3.StartupMessage, be *pgproto3.Backend, client net.Conn) error {
	be.Send(&pgproto3.AuthenticationCleartextPassword{})
	if err := be.Flush(); err != nil {
		return err
	}

	resp, err := be.Receive()
	if err != nil {
		return err
	}
	pw, ok := resp.(*pgproto3.PasswordMessage)
	if !ok {
		return fmt.Errorf("expected password message, got %T", resp)
	}

	user := sm.Parameters["user"]
	if user != p.local.User || pw.Password != p.local.Password {
		return errors.New("invalid local credentials")
	}
	return nil
}

func (p *Proxy) dialBackend(ctx context.Context, dbName string) (*pgconn.HijackedConn, error) {
	user, token, err := p.auth.Token(ctx)
	if err != nil {
		return nil, err
	}

	cfg, err := pgconn.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.Host = "127.0.0.1"
	cfg.Port = uint16(p.ssmLocalPort)
	cfg.Database = dbName
	cfg.User = user
	cfg.Password = token
	cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true} // matches DB_SSL_REJECT_UNAUTHORIZED=false
	cfg.ConnectTimeout = 15 * time.Second

	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := conn.SyncConn(ctx); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return conn.Hijack()
}

// completeFrontendHandshake tells the client it is authenticated, forwarding the
// real server's parameter statuses and backend key data, then ReadyForQuery.
func completeFrontendHandshake(be *pgproto3.Backend, hj *pgconn.HijackedConn) error {
	be.Send(&pgproto3.AuthenticationOk{})
	for k, v := range hj.ParameterStatuses {
		be.Send(&pgproto3.ParameterStatus{Name: k, Value: v})
	}
	be.Send(&pgproto3.BackendKeyData{ProcessID: hj.PID, SecretKey: hj.SecretKey})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: hj.TxStatus})
	return be.Flush()
}

// pipe copies bytes in both directions until either side closes.
func pipe(a, b net.Conn) error {
	errc := make(chan error, 2)
	go func() { _, err := io.Copy(a, b); errc <- err }()
	go func() { _, err := io.Copy(b, a); errc <- err }()
	err := <-errc
	_ = a.Close()
	_ = b.Close()
	if isClosed(err) {
		return nil
	}
	return err
}

func writeFatal(be *pgproto3.Backend, code, msg string) {
	be.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: msg})
	_ = be.Flush()
}

func isClosed(err error) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// SelfSignedTLS generates an in-memory self-signed cert/config for the proxy's
// client-facing TLS. Generate once and share across database listeners.
func SelfSignedTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

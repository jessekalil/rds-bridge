package awsiam

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Authenticator mints RDS IAM auth tokens for a single target. Token signing is
// offline (no network round-trip per call), so it is safe to mint one token per
// backend connection — which is exactly why no refresh loop is needed.
type Authenticator struct {
	region    string
	endpoint  string // token_host:token_port, used for the token signature
	profile   string
	userOverr string

	mu     sync.Mutex
	creds  aws.CredentialsProvider
	dbUser string
	loaded bool
}

// New builds an Authenticator for the given IAM profile/region. Credentials and
// the resolved DB user are loaded lazily on first Token call.
func New(region, profile, tokenHost string, tokenPort int, userOverride string) *Authenticator {
	return &Authenticator{
		region:    region,
		endpoint:  net.JoinHostPort(tokenHost, strconv.Itoa(tokenPort)),
		profile:   profile,
		userOverr: userOverride,
	}
}

func (a *Authenticator) load(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loaded {
		return nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(a.region),
		awsconfig.WithSharedConfigProfile(a.profile),
	)
	if err != nil {
		return fmt.Errorf("load aws config (profile %s): %w", a.profile, err)
	}
	a.creds = cfg.Credentials

	user := a.userOverr
	if user == "" {
		out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			return fmt.Errorf("resolve iam user via sts: %w", err)
		}
		if out.UserId == nil {
			return fmt.Errorf("sts returned empty UserId")
		}
		user = *out.UserId
	}
	a.dbUser = user
	a.loaded = true
	return nil
}

// User returns the resolved IAM DB username (loading credentials if needed).
func (a *Authenticator) User(ctx context.Context) (string, error) {
	if err := a.load(ctx); err != nil {
		return "", err
	}
	return a.dbUser, nil
}

// Token mints a fresh RDS IAM auth token to be used as the connection password.
func (a *Authenticator) Token(ctx context.Context) (user, token string, err error) {
	if err := a.load(ctx); err != nil {
		return "", "", err
	}
	tok, err := auth.BuildAuthToken(ctx, a.endpoint, a.region, a.dbUser, a.creds)
	if err != nil {
		return "", "", fmt.Errorf("build auth token: %w", err)
	}
	return a.dbUser, tok, nil
}

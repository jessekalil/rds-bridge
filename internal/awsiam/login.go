package awsiam

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CheckProfile verifies the profile has usable credentials by calling STS. A
// non-nil error means the profile needs an SSO login (or is misconfigured).
func CheckProfile(ctx context.Context, profile string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "aws", "--profile", profile,
		"sts", "get-caller-identity", "--output", "text", "--query", "Account")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ssoSession returns the sso_session name a profile references, or "" for the
// legacy inline SSO format (no sso_session).
func ssoSession(profile string) string {
	out, err := exec.Command("aws", "configure", "get", "sso_session", "--profile", profile).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// loginSession runs an interactive `aws sso login` for one SSO session.
func loginSession(session string) error {
	return runInteractive(exec.Command("aws", "sso", "login", "--sso-session", session))
}

// loginProfile runs an interactive `aws sso login` for a legacy profile.
func loginProfile(profile string) error {
	return runInteractive(exec.Command("aws", "sso", "login", "--profile", profile))
}

func runInteractive(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr // keep stdout clean (e.g. for `env` eval); URL goes to stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// EnsureProfiles makes sure every profile has usable credentials. Expired
// profiles are grouped by their SSO session and refreshed with a single
// `aws sso login` per session (a login authenticates the session, not the
// profile, so profiles sharing a session need only one login).
//
// When interactive is false (no TTY), it does not open a browser; it returns an
// error naming the expired profiles and the command to run.
func EnsureProfiles(ctx context.Context, profiles []string, interactive bool, logf func(format string, args ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	var expired []string
	for _, p := range uniqueStrings(profiles...) {
		if err := CheckProfile(ctx, p); err != nil {
			expired = append(expired, p)
		}
	}
	if len(expired) == 0 {
		return nil
	}

	// Map each expired profile to its SSO session (or "" for legacy).
	sessions := map[string]bool{}
	var legacy []string
	for _, p := range expired {
		if s := ssoSession(p); s != "" {
			sessions[s] = true
		} else {
			legacy = append(legacy, p)
		}
	}

	if !interactive {
		return fmt.Errorf("SSO login required for profile(s) %s; run: %s",
			strings.Join(expired, ", "), loginHint(sessions, legacy))
	}

	for s := range sessions {
		logf("SSO session %s expired — opening aws sso login", s)
		if err := loginSession(s); err != nil {
			return fmt.Errorf("aws sso login --sso-session %s: %w", s, err)
		}
	}
	for _, p := range legacy {
		logf("profile %s expired — opening aws sso login", p)
		if err := loginProfile(p); err != nil {
			return fmt.Errorf("aws sso login --profile %s: %w", p, err)
		}
	}

	// Confirm the login actually granted credentials.
	for _, p := range expired {
		if err := CheckProfile(ctx, p); err != nil {
			return fmt.Errorf("profile %s still has no valid credentials after login: %w", p, err)
		}
	}
	return nil
}

func loginHint(sessions map[string]bool, legacy []string) string {
	var parts []string
	for s := range sessions {
		parts = append(parts, "aws sso login --sso-session "+s)
	}
	for _, p := range legacy {
		parts = append(parts, "aws sso login --profile "+p)
	}
	return strings.Join(parts, " && ")
}

// uniqueStrings returns the input with duplicates removed, order preserved.
func uniqueStrings(in ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

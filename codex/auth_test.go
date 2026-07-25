package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// jwt builds an unsigned token with the given claims. Nothing in this package
// verifies signatures, so a real one would only make the fixtures unreadable.
func jwt(claims map[string]any) string {
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return encode(map[string]any{"alg": "none"}) + "." + encode(claims) + ".sig"
}

func accessToken(expiry time.Time) string {
	return jwt(map[string]any{"exp": expiry.Unix()})
}

func idToken(plan, account string) string {
	return jwt(map[string]any{authClaim: map[string]any{
		"chatgpt_plan_type": plan, "chatgpt_account_id": account,
	}})
}

// writeAuth lays down a credential file in the nested Codex CLI shape.
func writeAuth(t *testing.T, expiry time.Time, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	body := fmt.Sprintf(`{
  "OPENAI_API_KEY": null,
  "tokens": {
    "id_token": %q,
    "access_token": %q,
    "refresh_token": "rt-original",
    "account_id": "acct-1"%s
  },
  "last_refresh": "2026-07-01T00:00:00Z"
}`, idToken("plus", "acct-1"), accessToken(expiry), extra)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tokenServer answers refreshes, counting them and optionally failing.
func tokenServer(t *testing.T, expiry time.Time, fail bool) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostForm.Get("client_id"); got != ClientID {
			t.Errorf("client_id = %q, want %q", got, ClientID)
		}
		if got := r.PostForm.Get("refresh_token"); got != "rt-original" {
			t.Errorf("refresh_token = %q, want rt-original", got)
		}
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  accessToken(expiry),
			"refresh_token": "rt-rotated",
			"id_token":      idToken("plus", "acct-1"),
		})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestTokenRefreshesOnlyWhenDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		expiry  time.Time
		refresh bool
	}{
		{"expired", now.Add(-time.Hour), true},
		{"inside the early window", now.Add(2 * time.Minute), true},
		{"comfortably valid", now.Add(4 * time.Hour), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writeAuth(t, test.expiry, "")
			server, calls := tokenServer(t, now.Add(6*time.Hour), false)
			auth := &Auth{Path: path, TokenURL: server.URL, Now: func() time.Time { return now }}

			token, err := auth.Token(context.Background())
			if err != nil {
				t.Fatalf("Token: %v", err)
			}
			if want := boolToInt(test.refresh); *calls != want {
				t.Errorf("refresh calls = %d, want %d", *calls, want)
			}
			if token.PlanType != "plus" || token.AccountID != "acct-1" {
				t.Errorf("token = %+v, want plan plus and account acct-1", token)
			}
			if test.refresh && token.Expired(now) {
				t.Error("refreshed token is still expired")
			}
		})
	}
}

func TestFailedRefreshProceedsOnALiveToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	path := writeAuth(t, now.Add(2*time.Minute), "")
	server, _ := tokenServer(t, now, true)
	var logged int
	auth := &Auth{
		Path: path, TokenURL: server.URL, Now: func() time.Time { return now },
		Logf: func(string, ...any) { logged++ },
	}

	token, err := auth.Token(context.Background())
	if err != nil {
		t.Fatalf("a token two minutes from expiry must still be usable: %v", err)
	}
	if token.Access == "" {
		t.Error("no access token returned")
	}
	// A refresh that keeps failing must not narrate it once per request.
	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if logged != 1 {
		t.Errorf("logged %d refresh failures, want exactly 1", logged)
	}
}

func TestFailedRefreshOnADeadTokenIsAnError(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	path := writeAuth(t, now.Add(-time.Hour), "")
	server, _ := tokenServer(t, now, true)
	auth := &Auth{Path: path, TokenURL: server.URL, Now: func() time.Time { return now }}

	if _, err := auth.Token(context.Background()); err == nil {
		t.Fatal("an expired token with a failed refresh must not be reported as usable")
	}
}

func TestWritePreservesUnknownFieldsAndLeavesABackup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	// A field this code has never heard of, which the Codex CLI may rely on.
	path := writeAuth(t, now.Add(-time.Hour), `,
    "some_future_field": {"nested": [1, 2]}`)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := tokenServer(t, now.Add(6*time.Hour), false)
	auth := &Auth{Path: path, TokenURL: server.URL, Now: func() time.Time { return now }}
	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}

	var written map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("rewritten file does not parse: %v", err)
	}
	if _, ok := written["OPENAI_API_KEY"]; !ok {
		t.Error("OPENAI_API_KEY was dropped; it is null, not absent")
	}
	tokens, ok := written["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens = %T, want an object", written["tokens"])
	}
	if _, ok := tokens["some_future_field"]; !ok {
		t.Error("an unknown field inside tokens was dropped")
	}
	if tokens["refresh_token"] != "rt-rotated" {
		t.Errorf("refresh_token = %v, want the rotated one", tokens["refresh_token"])
	}
	if written["last_refresh"] == "2026-07-01T00:00:00Z" {
		t.Error("last_refresh was not updated")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(backup) != string(original) {
		t.Error("backup does not match the file that was replaced")
	}
	if _, err := os.Stat(path + ".lock"); err == nil {
		t.Error("lock file was left behind")
	}
}

// The flat layout is what a tool that stores the raw OAuth token response
// leaves behind. It has no account_id, so the account has to come from the
// id token's claims.
func TestFlatCredentialLayoutIsRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "codex-credentials.json")
	body := fmt.Sprintf(`{"access_token": %q, "id_token": %q, "refresh_token": "rt-flat",
  "token_type": "Bearer", "expires_in": 864000}`,
		accessToken(now.Add(4*time.Hour)), idToken("free", "acct-flat"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Path: path, Now: func() time.Time { return now }}

	token, err := auth.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token.PlanType != "free" {
		t.Errorf("plan = %q, want free", token.PlanType)
	}
	if token.AccountID != "acct-flat" {
		t.Errorf("account = %q, want acct-flat from the id token claims", token.AccountID)
	}
}

func TestMissingCredentialSaysWhatToDo(t *testing.T) {
	t.Parallel()
	auth := &Auth{Path: filepath.Join(t.TempDir(), "absent.json")}
	_, err := auth.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error for a missing credential")
	}
	if got := err.Error(); !strings.Contains(got, "login") {
		t.Errorf("error = %q, want it to name the login step", got)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

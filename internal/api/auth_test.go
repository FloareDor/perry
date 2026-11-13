package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequireTokenCoversEveryRoute is a regression test for an authentication
// hole, not a unit test for a helper.
//
// `perry serve -token x` used to mount the authenticated read API at
// /v1/repos/ and the git smart HTTP handler at / with no check at all. Git
// smart HTTP carries receive-pack, so the unauthenticated route was not a read
// leak but an anonymous *write*: anyone who could reach the port could push to
// any repository, and the server created repositories on demand. Setting a
// token made the server look protected while it accepted anonymous pushes.
//
// The paths below are therefore the point of the test. git-receive-pack must
// be refused.
func TestRequireTokenCoversEveryRoute(t *testing.T) {
	const token = "secret-token"

	var reached []string
	inner := http.NewServeMux()
	inner.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(RequireToken(inner, token))
	defer srv.Close()

	paths := []string{
		"/v1/repos/demo/grep?q=x",
		"/demo.git/info/refs?service=git-upload-pack",
		"/demo.git/info/refs?service=git-receive-pack",
		"/demo.git/git-receive-pack",
		"/demo.git/git-upload-pack",
	}

	for _, p := range paths {
		req, err := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a token: got %d, want 401", p, resp.StatusCode)
		}
	}

	if len(reached) != 0 {
		t.Errorf("unauthenticated requests reached the handler: %v", reached)
	}
}

// TestRequireTokenAcceptsBearerAndBasic covers both forms. Git cannot send a
// bearer token, so Basic with the token as the password has to work or the
// git routes become unusable once a token is set.
func TestRequireTokenAcceptsBearerAndBasic(t *testing.T) {
	const token = "secret-token"

	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	srv := httptest.NewServer(RequireToken(http.HandlerFunc(ok), token))
	defer srv.Close()

	cases := []struct {
		name string
		set  func(*http.Request)
		want int
	}{
		{"bearer", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		}, http.StatusNoContent},
		{"basic, token as password", func(r *http.Request) {
			r.SetBasicAuth("anything", token)
		}, http.StatusNoContent},
		{"basic, token as username", func(r *http.Request) {
			r.SetBasicAuth(token, "")
		}, http.StatusUnauthorized},
		{"wrong bearer", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer nope")
		}, http.StatusUnauthorized},
		{"wrong basic", func(r *http.Request) {
			r.SetBasicAuth("x", "nope")
		}, http.StatusUnauthorized},
		{"nothing", func(*http.Request) {}, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/demo.git/git-receive-pack", nil)
			if err != nil {
				t.Fatal(err)
			}
			tc.set(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("got %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestRequireTokenChallenges checks the 401 carries WWW-Authenticate. Without
// it git fails outright instead of consulting a credential helper, so a
// tokenised server would be unusable from git even with correct credentials
// configured.
func TestRequireTokenChallenges(t *testing.T) {
	srv := httptest.NewServer(RequireToken(http.NotFoundHandler(), "secret"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/demo.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("WWW-Authenticate"); got != `Basic realm="perry"` {
		t.Errorf("WWW-Authenticate = %q, want Basic realm=\"perry\"", got)
	}
}

// TestRequireTokenEmptyIsPassThrough keeps the local single-user default
// working: no token means no check and no wrapper.
func TestRequireTokenEmptyIsPassThrough(t *testing.T) {
	inner := http.NotFoundHandler()
	if got := RequireToken(inner, ""); got == nil {
		t.Fatal("RequireToken with an empty token returned nil")
	}

	srv := httptest.NewServer(RequireToken(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/demo.git/git-receive-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("got %d, want 204 with no token configured", resp.StatusCode)
	}
}

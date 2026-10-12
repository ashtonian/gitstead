package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
)

func TestListOwners_GitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/user":
			fmt.Fprint(w, `{"login": "me"}`)
		case "/api/v3/user/orgs":
			fmt.Fprint(w, `[{"login": "acme"}, {"login": "acme-labs"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	owners, err := ListOwners(context.Background(), config.Provider{Name: "gh", Type: "github", URL: srv.URL + "/api/v3/", Token: "t"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(owners); got != "[{me self} {acme org} {acme-labs org}]" {
		t.Errorf("owners = %s", got)
	}
}

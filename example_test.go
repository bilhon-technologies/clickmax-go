package clickmax_test

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"clickmax.io/go"
)

func ExampleNew() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"), clickmax.WithTimeout(30*time.Second))
	if err != nil {
		log.Fatal(err)
	}

	tags, err := c.Tags.List(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d tags", len(tags.Data))
}

func ExampleNewApp() {
	// The installation comes from ExchangeInstallCode, stored with the
	// workspace when it installed the app.
	installationID := "0b6f3c3e-1d1f-4a53-9a51-6f3d7f0a1a11"

	c, err := clickmax.NewApp(clickmax.AppCredentials{
		ClientID:       os.Getenv("CLICKMAX_CLIENT_ID"),
		ClientSecret:   os.Getenv("CLICKMAX_CLIENT_SECRET"),
		InstallationID: installationID,
	})
	if err != nil {
		log.Fatal(err)
	}

	tags, err := c.Tags.List(context.Background())
	var cerr *clickmax.Error
	if errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeAuthentication {
		log.Fatalf("credentials or installation refused: code=%s", cerr.Code)
	}
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d tags", len(tags.Data))
}

// session stands for the application's session store, which keeps the state
// and PKCE verifier generated with the install link.
type session struct{ State, CodeVerifier string }

type sessionStore struct{}

func (sessionStore) Get(*http.Request) session { return session{} }

var sessions sessionStore

func ExampleExchangeInstallCode() {
	http.HandleFunc("/clickmax/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sess := sessions.Get(r)
		if q.Get("code") == "" || q.Get("state") != sess.State {
			http.Error(w, "app not installed", http.StatusBadRequest)
			return
		}

		out, err := clickmax.ExchangeInstallCode(r.Context(), clickmax.InstallCodeExchangeInput{
			ClientID:     os.Getenv("CLICKMAX_CLIENT_ID"),
			ClientSecret: os.Getenv("CLICKMAX_CLIENT_SECRET"),
			Code:         q.Get("code"),
			CodeVerifier: sess.CodeVerifier,
			RedirectURI:  "https://app.example.com/clickmax/callback",
		})
		if err != nil {
			http.Error(w, "install failed", http.StatusBadGateway)
			return
		}
		// Store out.InstallationID with the workspace; NewApp needs it.
		log.Printf("installed in workspace %s as %s", out.WorkspaceID, out.InstallationID)
	})
}

func ExampleTagsService_ManualList() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	page, err := c.Tags.ManualList(context.Background(), clickmax.TagsManualListParams{
		Name:    clickmax.Ptr("vip"),
		PerPage: clickmax.Ptr[int64](50),
		Order:   clickmax.Ptr(clickmax.TagsManualListParamsOrderDesc),
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%+v", page)
}

func ExampleNullable() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	// Clears the description, sets the color and leaves the group name unchanged.
	_, err = c.Pipelines.Update(context.Background(), clickmax.PipelinesUpdateInput{
		ID:          "0b7f4c1e-5d2a-4f3b-9c8d-1a2b3c4d5e6f",
		Description: clickmax.Null[string](),
		Color:       clickmax.NewNullable("ff8800"),
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleError() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	_, err = c.Tags.Create(context.Background(), clickmax.TagsCreateInput{Name: "VIP"})
	var cerr *clickmax.Error
	switch {
	case err == nil:
	case errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeInvalidRequest:
		log.Printf("input rejected: code=%s issues=%v", cerr.Code, cerr.Issues)
	case errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeRateLimit:
		log.Printf("rate limited, retry in %s", cerr.RetryAfter)
	default:
		log.Printf("tags create failed: %v", err)
	}
}

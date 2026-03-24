package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

var (
	oauthConfig *oauth2.Config
	jobs        sync.Map // map[string]*Job
)

// Job holds the state of a scraping job keyed by a random ID.
type Job struct {
	Status  string        `json:"status"` // "running" | "done" | "error"
	Results []Application `json:"results,omitempty"`
	Error   string        `json:"error,omitempty"`
}

func main() {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	baseURL := os.Getenv("APP_BASE_URL")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if clientID == "" || clientSecret == "" || baseURL == "" {
		log.Fatal("GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, and APP_BASE_URL must be set")
	}

	oauthConfig = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  baseURL + "/auth/callback",
		Scopes:       []string{gmail.GmailReadonlyScope},
		Endpoint:     google.Endpoint,
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "static/index.html")
	})
	http.HandleFunc("/results", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/index.html")
	})
	http.HandleFunc("/auth/login", handleLogin)
	http.HandleFunc("/auth/callback", handleCallback)
	http.HandleFunc("/api/status", handleStatus)

	log.Printf("Server starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// handleLogin generates a random CSRF state, stores it in a cookie, and
// redirects the user to Google's OAuth consent screen.
func handleLogin(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := base64.URLEncoding.EncodeToString(b)

	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})

	url := oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// handleCallback receives the OAuth code from Google, validates the CSRF state,
// exchanges the code for a token, and starts the Gmail scrape in the background.
func handleCallback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("oauth_state")
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}

	state := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	// Clear the state cookie immediately after validation.
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", MaxAge: -1, Path: "/"})

	code := r.URL.Query().Get("code")
	token, err := oauthConfig.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jobID := newJobID()
	jobs.Store(jobID, &Job{Status: "running"})
	go runScrape(jobID, token)

	http.Redirect(w, r, "/results?job="+jobID, http.StatusTemporaryRedirect)
}

// handleStatus returns the current state of a scraping job as JSON.
// The job is deleted from memory once a terminal state (done/error) is returned.
func handleStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job")
	val, ok := jobs.Load(jobID)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"status":"error","error":"job not found"}`)
		return
	}

	job := val.(*Job)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)

	if job.Status == "done" || job.Status == "error" {
		jobs.Delete(jobID)
	}
}

// runScrape builds a Gmail service from the OAuth token and runs ScrapeApplications,
// updating the job entry when complete.
func runScrape(jobID string, token *oauth2.Token) {
	ctx := context.Background()
	httpClient := oauthConfig.Client(ctx, token)
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		jobs.Store(jobID, &Job{Status: "error", Error: "failed to create Gmail service"})
		return
	}

	results, err := ScrapeApplications(ctx, srv)
	if err != nil {
		jobs.Store(jobID, &Job{Status: "error", Error: err.Error()})
		return
	}

	jobs.Store(jobID, &Job{Status: "done", Results: results})
}

// newJobID returns a random 32-char hex string used as a job identifier.
func newJobID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

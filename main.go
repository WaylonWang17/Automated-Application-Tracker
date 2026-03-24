package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

var (
	oauthConfig   *oauth2.Config
	sessionSecret string
	db            *pgx.Conn
	jobs          sync.Map // map[string]*Job
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
	sessionSecret = os.Getenv("SESSION_SECRET")
	dbURL := os.Getenv("DATABASE_URL")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if clientID == "" || clientSecret == "" || baseURL == "" {
		log.Fatal("GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, and APP_BASE_URL must be set")
	}
	if sessionSecret == "" {
		log.Fatal("SESSION_SECRET must be set")
	}

	if dbURL != "" {
		var err error
		db, err = initDB(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("Database init failed: %v", err)
		}
		defer db.Close(context.Background())
		log.Println("Database connected")
	} else {
		log.Println("Warning: DATABASE_URL not set — results will not be persisted")
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
	http.HandleFunc("/api/me", handleMe)
	http.HandleFunc("/api/my-results", handleMyResults)

	log.Printf("Server starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// handleLogin stores the CSRF state and optional since-date cookie, then redirects to Google.
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

	// Persist the chosen start date through the OAuth redirect.
	sinceDate := r.URL.Query().Get("since")
	if sinceDate == "" {
		sinceDate = "2024-08-01"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "scan_since",
		Value:    sinceDate,
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})

	url := oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.SetAuthURLParam("prompt", "select_account"))
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// handleCallback validates the OAuth state, exchanges the code, gets the user's email,
// sets a session cookie, and launches the scrape goroutine.
func handleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(r.URL.Query().Get("state"))) != 1 {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", MaxAge: -1, Path: "/"})

	sinceCookie, _ := r.Cookie("scan_since")
	sinceDate := "2024-08-01"
	if sinceCookie != nil && sinceCookie.Value != "" {
		sinceDate = sinceCookie.Value
	}
	http.SetCookie(w, &http.Cookie{Name: "scan_since", MaxAge: -1, Path: "/"})

	token, err := oauthConfig.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the user's email address before launching the goroutine so we can set the session cookie now.
	httpClient := oauthConfig.Client(r.Context(), token)
	srv, err := gmail.NewService(r.Context(), option.WithHTTPClient(httpClient))
	if err != nil {
		http.Error(w, "failed to create Gmail service", http.StatusInternalServerError)
		return
	}
	profile, err := srv.Users.GetProfile("me").Do()
	if err != nil {
		http.Error(w, "failed to get Gmail profile", http.StatusInternalServerError)
		return
	}
	email := profile.EmailAddress

	// Set session cookie immediately — the email is known even before the scrape finishes.
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    signSession(email, sessionSecret),
		MaxAge:   30 * 24 * 3600, // 30 days
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})

	jobID := newJobID()
	jobs.Store(jobID, &Job{Status: "running"})
	go runScrape(jobID, token, email, sinceDate)

	http.Redirect(w, r, "/results?job="+jobID, http.StatusTemporaryRedirect)
}

// handleStatus returns the current state of a scraping job as JSON.
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

// handleMe returns the current user's info from the session cookie, or 401 if not logged in.
func handleMe(w http.ResponseWriter, r *http.Request) {
	email, ok := sessionEmail(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if db == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"email": email})
		return
	}

	info, err := getUserInfo(r.Context(), db, email)
	if err != nil || info == nil {
		// User authenticated but no DB record yet (scan in progress or first time).
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"email": email})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleMyResults returns the user's saved applications from the database.
func handleMyResults(w http.ResponseWriter, r *http.Request) {
	email, ok := sessionEmail(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if db == nil {
		http.Error(w, "database not available", http.StatusServiceUnavailable)
		return
	}

	apps, err := getApplicationsFromDB(r.Context(), db, email)
	if err != nil {
		http.Error(w, "failed to fetch results", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apps)
}

// runScrape runs the Gmail scan, saves results to DB, and updates the job entry.
func runScrape(jobID string, token *oauth2.Token, email, sinceDate string) {
	ctx := context.Background()
	httpClient := oauthConfig.Client(ctx, token)
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		jobs.Store(jobID, &Job{Status: "error", Error: "failed to create Gmail service"})
		return
	}

	results, err := ScrapeApplications(ctx, srv, sinceDate)
	if err != nil {
		jobs.Store(jobID, &Job{Status: "error", Error: err.Error()})
		return
	}

	if db != nil {
		if err := upsertUser(ctx, db, email, sinceDate); err != nil {
			log.Printf("upsertUser %s: %v", email, err)
		}
		if err := replaceApplications(ctx, db, email, results); err != nil {
			log.Printf("replaceApplications %s: %v", email, err)
		}
	}

	jobs.Store(jobID, &Job{Status: "done", Results: results})
}

// --- session helpers ---

func signSession(email, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(email))
	sig := hex.EncodeToString(mac.Sum(nil))
	return email + "." + sig
}

func verifySession(value, secret string) (email string, ok bool) {
	dot := strings.LastIndex(value, ".")
	if dot < 0 {
		return "", false
	}
	email = value[:dot]
	expected := signSession(email, secret)
	if subtle.ConstantTimeCompare([]byte(value), []byte(expected)) != 1 {
		return "", false
	}
	return email, true
}

func sessionEmail(r *http.Request) (string, bool) {
	cookie, err := r.Cookie("session")
	if err != nil {
		return "", false
	}
	return verifySession(cookie.Value, sessionSecret)
}

// --- misc ---

func newJobID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

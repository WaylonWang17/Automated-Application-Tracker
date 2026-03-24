# Job Application Tracker

A web app that connects to your Gmail and automatically finds all your job application confirmation emails, organized in one place.

**Live app:** https://job-application-tracker-hbnr.onrender.com

## What it does

- Connects to your Gmail via Google OAuth (read-only access)
- Searches for job application confirmation emails (e.g. "Thank you for applying", "We received your application", etc.)
- Lets you choose how far back to scan
- Saves your results so you can return without re-scanning
- Exports results as JSON or CSV

## How to use

1. Visit the app URL
2. Choose a start date for the scan (default: August 2024)
3. Click **Connect Gmail** and authorize read-only access
4. Wait 30–60 seconds while your inbox is scanned
5. View your results — download as JSON or CSV if needed
6. Return any time to view your saved results without re-scanning

## Local development

**Prerequisites:** Go 1.24+, a Google Cloud project with Gmail API enabled, and a Web application OAuth 2.0 client.

Set environment variables:
```bash
export GOOGLE_CLIENT_ID=your_client_id
export GOOGLE_CLIENT_SECRET=your_client_secret
export APP_BASE_URL=http://localhost:8080
export SESSION_SECRET=any_random_string
# DATABASE_URL is optional locally — results won't be persisted without it
```

Run:
```bash
go run .
```

Visit `http://localhost:8080`.

Add `http://localhost:8080/auth/callback` as an Authorized Redirect URI in your Google Cloud OAuth client.

## Deployment (Render)

1. Fork/clone this repo and push to GitHub
2. Create a new Web Service on [Render](https://render.com) connected to your repo
3. Add a PostgreSQL database in the same Render project
4. Set environment variables in the Render dashboard:
   - `GOOGLE_CLIENT_ID`
   - `GOOGLE_CLIENT_SECRET`
   - `APP_BASE_URL` — your Render service URL (e.g. `https://your-service.onrender.com`)
   - `SESSION_SECRET` — any random string (use Render's Generate button)
   - `DATABASE_URL` — copy the Internal Database URL from your Render PostgreSQL instance
5. Add your Render URL's `/auth/callback` as an Authorized Redirect URI in Google Cloud Console

## Tech stack

- **Backend:** Go (standard library + Gmail API + pgx)
- **Frontend:** Plain HTML/CSS/JS — no framework
- **Database:** PostgreSQL
- **Auth:** Google OAuth 2.0 (read-only Gmail scope)
- **Hosting:** Render

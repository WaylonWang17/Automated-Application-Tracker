package main

import (
	"context"
	"fmt"

	"google.golang.org/api/gmail/v1"
)

const gmailQuery = `after:2024/8/01 ("thank you for applying" OR "we received your application" OR "application submitted" OR "thank you for your interest" OR "we have received your application" OR "your application has been received" OR "we appreciate your interest" OR "application confirmation" OR "we have received your resume" OR "your application is being reviewed" OR "we are reviewing your application" OR "we have your application" OR "your application has been submitted" OR "thank you for your application" OR "we have received your job application") -in:chats -is:sent`

// Application represents a single job application email.
type Application struct {
	Subject string `json:"subject"`
	From    string `json:"from"`
	Date    string `json:"date"`
	Snippet string `json:"snippet"`
}

// ScrapeApplications searches the authenticated user's Gmail for job application
// confirmation emails and returns them. Pagination is handled automatically.
func ScrapeApplications(ctx context.Context, srv *gmail.Service) ([]Application, error) {
	pageToken := ""
	var results []Application

	for {
		req := srv.Users.Messages.List("me").Q(gmailQuery).IncludeSpamTrash(false)
		if pageToken != "" {
			req.PageToken(pageToken)
		}

		res, err := req.Do()
		if err != nil {
			return nil, fmt.Errorf("list messages: %w", err)
		}

		for _, msg := range res.Messages {
			msgDetail, err := srv.Users.Messages.Get("me", msg.Id).Format("full").Do()
			if err != nil {
				continue
			}

			var subject, from, date string
			for _, header := range msgDetail.Payload.Headers {
				switch header.Name {
				case "Subject":
					subject = header.Value
				case "From":
					from = header.Value
				case "Date":
					date = header.Value
				}
			}

			results = append(results, Application{
				Subject: subject,
				From:    from,
				Date:    date,
				Snippet: msgDetail.Snippet,
			})
		}

		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}

	return results, nil
}

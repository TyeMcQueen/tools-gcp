/*
This package provides wrappers to make it easier to get an authenticated
connection to GCP services.
*/
package conn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)


var projectID = ""


// Gets a GCP project name from FindDefaultCredentials() (from
// golang.org/x/oauth2/google).  Though, if a project ID was previously
// obtained such as via GcloudDefaultProject(), then that is just returned.
func DefaultProjectId() string {
	if "" == projectID {
		creds, err := google.FindDefaultCredentials(
			context.Background(),
			"https://www.googleapis.com/auth/compute",
		)
		if nil == err {
			projectID = creds.ProjectID
		} else {
			projectID = GcloudDefaultProject()
		}
	}
	return projectID
}


// Runs the gcloud command to get the project name that gcloud will connect
// to by default.
func GcloudDefaultProject() string {
	type gcloudConfig struct {
		Core struct {
			Project string
		}
	}
	config := gcloudConfig{}
	cmd := exec.Command("gcloud", "config", "list", "--format=json")
	o := new(bytes.Buffer)      // Save Stdout to a buffer.
	cmd.Stdout = o
	e := new(bytes.Buffer)      // Save Stderr to a buffer.
	cmd.Stderr = e

	if err := cmd.Run(); nil != err {
		lager.Fail().Map("Can't run gcloud", err)
		return ""
	}
	if 0 < e.Len() {
		lager.Fail().Map("gcloud complained", e.Bytes())
		return ""
	}
	lager.Debug().Map("Gcloud output", o.Bytes())
	if err := json.Unmarshal(o.Bytes(), &config); nil != err {
		lager.Fail().Map("Error parsing gcloud JSON", err)
		return ""
	}
	lager.Debug().Map("Parsed gcloud output", config)

	return config.Core.Project
}


// Runs the gcloud command to get the access token that it uses and makes
// a connection that uses that for auth to GCP services.  Passing in OAuth
// scopes as extra arguments causes those scopes to be used in the connection.
// However, it is frankly not known to the author if or when those scopes
// have any actual impact.
func GcloudAccessClient(
	ctx context.Context, scopes ...string,
) (*http.Client, error) {
	if nil == ctx {
		ctx = context.Background()
	}
	type gcloudAuth struct {
		Access_token    string
		Client_id       string
		Client_secret   string
		Refresh_token   string
		Scopes          []string
		Token_uri       string
		Token_expiry    struct {
			Datetime    string
		}
		Token_response  struct {
			Token_type  string
		}
	}
	auth := gcloudAuth{}

	cmd := exec.Command("gcloud", "auth", "print-access-token", "--format=json")
	o := new(bytes.Buffer)      // Save Stdout to a buffer.
	cmd.Stdout = o
	e := new(bytes.Buffer)      // Save Stderr to a buffer.
	cmd.Stderr = e

	if err := cmd.Run(); nil != err {
		return nil, fmt.Errorf("Error running gcloud: %v", err)
	}
	if 0 < e.Len() {
		return nil, fmt.Errorf("gcloud complained: %v", e.Bytes())
	}
	lager.Debug().Map("Gcloud output", o.Bytes())
	if err := json.Unmarshal(o.Bytes(), &auth); nil != err {
		return nil, fmt.Errorf("Error parsing gcloud JSON: %v", err)
	}
	lager.Debug().Map("Parsed gcloud output", auth)

	expiry, err := time.Parse(
		"2006-01-02 15:04:05.9", auth.Token_expiry.Datetime)
	if nil != err {
		return nil, fmt.Errorf("Invalid expiry time: %v", err)
	}
	token := oauth2.Token{
		AccessToken:    auth.Access_token,
		TokenType:      auth.Token_response.Token_type,
		RefreshToken:   auth.Refresh_token,
		Expiry:         expiry,
	}
	lager.Debug().Map("Token", token)
	if 0 < len(scopes) {
		auth.Scopes = scopes
	}
	conf := oauth2.Config{
		ClientID:       auth.Client_id,
		ClientSecret:   auth.Client_secret,
		Endpoint:       oauth2.Endpoint{
			TokenURL:   auth.Token_uri,
		//  token_uri:  "https://www.googleapis.com/oauth2/v4/token",
			AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		},
		Scopes:         auth.Scopes,
	}
	return conf.Client(ctx, &token), nil
}


// Uses DefaultClient() (from golang.org/x/oauth2/google) to obtain
// authentication for GCP services.  If that fails, then falls back to
// GcloudAccessClient().
func GoogleClient(
	ctx context.Context, scopes ...string,
) (*http.Client, error) {
	if nil == ctx {
		ctx = context.Background()
	}
	defaultScopes := scopes
	if 0 == len(scopes) {
		defaultScopes = []string{
			"https://www.googleapis.com/auth/accounts.reauth",
			"https://www.googleapis.com/auth/appengine.admin",
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/compute",
			"https://www.googleapis.com/auth/userinfo.email",
		}
	}
	gcpClient, err := google.DefaultClient(ctx, defaultScopes...)
	if err != nil {
		gcpClient, err = GcloudAccessClient(ctx, scopes...)
	}
	if err != nil {
		return nil, err
	}
	return gcpClient, nil
}


// Calls GoogleClient() but, if that fails, it reports the error and exits.
func MustGoogleClient() *http.Client {
	gcpClient, err := GoogleClient(context.Background())
	if nil != err {
		lager.Exit().Map("Can't configure access to GCP APIs", err)
	}
	return gcpClient
}

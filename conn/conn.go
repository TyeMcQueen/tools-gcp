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
			"https://www.googleapis.com/auth/monitoring.read",
		)
		if nil == err {
			projectID = creds.ProjectID
			lager.Info().Map("GCP ProjectID from default creds", projectID)
		} else {
			projectID = GcloudDefaultProject()
			if "" != projectID {
				lager.Info().Map("GCP ProjectID from gcloud", projectID)
			}
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
// a connection that uses that for auth to GCP services.  Token will not
// be renewed so this access will only work for a relatively short time.
func GcloudAccessClient(ctx context.Context) (*http.Client, error) {
	if nil == ctx {
		ctx = context.Background()
	}

	cmd := exec.Command("gcloud", "auth", "print-access-token")
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

	accToken := o.Bytes()
	if '\n' == accToken[len(accToken)-1] {
		accToken = accToken[:len(accToken)-1]
	}
	lager.Debug().Map("Gcloud access token", accToken)

	client := oauth2.NewClient(
		ctx,
		oauth2.StaticTokenSource(
			&oauth2.Token{
				TokenType:      "Bearer",
				AccessToken:    string(accToken),
			},
		),
	)

	return client, nil
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
		gcpClient, err = GcloudAccessClient(ctx)
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

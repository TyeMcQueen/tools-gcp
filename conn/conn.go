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
	"os"
	"os/exec"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const ZuluTime = "2006-01-02T15:04:05Z"

func TimeAsString(when time.Time) string {
	return when.In(time.UTC).Format(ZuluTime)
}

func EnvDuration(envVar, defaultDur string) time.Duration {
	if durStr := os.Getenv(envVar); "" != durStr {
		dur, err := time.ParseDuration(durStr)
		if nil == err {
			return dur
		}
		lager.Warn().MMap("Invalid duration from environment",
			"envVar", envVar, "value", durStr, "Error", err)
		os.Setenv(envVar, defaultDur)
	}
	dur, err := time.ParseDuration(defaultDur)
	if nil == err {
		return dur
	}
	lager.Exit().WithStack(1, 2).MMap("Invalid default duration in code",
		"envVar", envVar, "value", defaultDur, "Error", err)
	return time.Duration(0) // Not reached.
}

func Timeout(pCtx *context.Context, dur time.Duration) context.CancelFunc {
	if nil == pCtx {
		lager.Exit().WithCaller(1).MMap(
			"Nil pointer to Context passed to Timeout()")
	}
	if nil == *pCtx {
		*pCtx = context.Background()
	}
	ctx, can := context.WithTimeout(*pCtx, dur)
	*pCtx = ctx
	return can
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
	o := new(bytes.Buffer) // Save Stdout to a buffer.
	cmd.Stdout = o
	e := new(bytes.Buffer) // Save Stderr to a buffer.
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
	o := new(bytes.Buffer) // Save Stdout to a buffer.
	cmd.Stdout = o
	e := new(bytes.Buffer) // Save Stderr to a buffer.
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
				TokenType:   "Bearer",
				AccessToken: string(accToken),
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

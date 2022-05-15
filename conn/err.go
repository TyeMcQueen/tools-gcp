package conn

import (
	"google.golang.org/api/googleapi"
)

// Returns 200 if `err` is `nil`.  If `err` is a googleapi.Error, then returns
// the HTTP status code from it.  Otherwise, returns 0.
func ErrorCode(err error) int {
	if nil == err {
		return 200
	}
	if apiErr, ok := err.(*googleapi.Error); ok {
		return apiErr.Code
	}
	return 0
}

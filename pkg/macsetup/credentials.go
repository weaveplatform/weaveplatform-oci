package macsetup

import (
	"errors"
	"fmt"
	"os"
)

// SSHCredentials applies explicit overrides to the saved setup account. Older
// images retain the original weave/weave defaults when no setup record exists.
func SSHCredentials(path, user, password string) (string, string, error) {
	if user != "" && password != "" {
		return user, password, nil
	}
	saved, err := Load(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if err == nil {
		if user != "" && user != saved.User && password == "" {
			return "", "", fmt.Errorf(
				"%w: specify a password for an alternate SSH user",
				ErrOnboarding,
			)
		}
		if user == "" {
			user = saved.User
		}
		if password == "" {
			password = saved.Password
		}
	} else {
		if user == "" {
			user = "weave"
		}
		if password == "" {
			password = "weave"
		}
	}
	return user, password, nil
}

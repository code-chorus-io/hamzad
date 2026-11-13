// Package session moves a profile's browsing session in and out of its shared
// bundle: the user-data-dir files, plus its cookies as portable JSON.
//
// The files alone are not enough. Chrome encrypts cookie values with a key the
// operating system holds per machine, so the Cookies database a teammate
// restores is unreadable on their machine and the account arrives logged out.
// Save therefore also exports the cookies in plaintext through the browser
// (inside the encrypted bundle only), and Restore writes them back through the
// browser, which encrypts them under the local key. See chrome.ExportCookies.
package session

import (
	"context"
	"fmt"

	"github.com/code-chorus-io/hamzad/internal/infra/chrome"
	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// Result reports what a Save or Restore did with the cookies.
type Result struct {
	// Cookies is how many cookies were exported (Save) or imported (Restore).
	Cookies int
	// Refused is how many exported cookies the browser would not take back.
	Refused int
	// CookieErr is set when the cookie step failed but the bundle step did not.
	// The session is still saved or restored, without portable cookies; the
	// caller should warn, not fail.
	CookieErr error
}

// Save exports the profile's cookies with the browser at execPath and packs
// the working data with them into the encrypted bundle.
//
// A failed export does not stop the save: the bundle is written without the
// cookies JSON, which is what every bundle held before, and still restores in
// full on this machine. An empty execPath skips the export.
func Save(ctx context.Context, st *store.Store, c crypt.Crypt, execPath, name string) (Result, error) {
	var res Result

	var cookies []byte
	if execPath == "" {
		res.CookieErr = chrome.ErrNoBrowser
	} else {
		data, n, err := chrome.ExportCookies(ctx, execPath, st.UserDataDir(name))
		if err != nil {
			res.CookieErr = fmt.Errorf("exporting cookies: %w", err)
		} else {
			cookies, res.Cookies = data, n
		}
	}

	if err := st.PackBundleWith(c, name, cookies); err != nil {
		return res, err
	}

	return res, nil
}

// Restore unpacks the profile's bundle over its working data and, when the
// bundle carries exported cookies, writes them into a fresh cookie store with
// the browser at execPath.
//
// A failed import does not undo the restore; the profile is usable, but signed
// out of whatever its cookies held.
func Restore(ctx context.Context, st *store.Store, c crypt.Crypt, execPath, name string) (Result, error) {
	var res Result

	cookies, err := st.UnpackBundleWith(c, name)
	if err != nil {
		return res, err
	}
	if cookies == nil {
		return res, nil
	}

	if execPath == "" {
		res.CookieErr = chrome.ErrNoBrowser

		return res, nil
	}

	n, refused, err := chrome.ImportCookies(ctx, execPath, st.UserDataDir(name), cookies)
	res.Cookies, res.Refused = n, refused
	if err != nil {
		res.CookieErr = fmt.Errorf("importing cookies: %w", err)
	}

	return res, nil
}

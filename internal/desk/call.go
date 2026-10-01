//go:build !js

package desk

import (
	"context"
	"errors"
	"net/url"
	"regexp"

	"github.com/richardwooding/parley/phrase"

	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/xfer"
)

// ConfabURL is the hosted confab, the video-call sibling of satchel.
const ConfabURL = "https://confab-call.fly.dev"

// callItem names the single item of a call invite. A peer chooses the
// name, so it only decides how the offer is presented; what is opened is
// decided by validating the item's content (see CallLink).
const callItem = "confab-call"

// callPhrase is confab's phrase shape, word-NN-word.
var callPhrase = regexp.MustCompile(`^[a-z]+-[0-9]{1,3}-[a-z]+$`)

// CallLink reports whether s is exactly a confab join link and returns it
// rebuilt from its parts. A paired device can send any bytes it likes as an
// "invite"; only this shape is ever handed to a browser.
func CallLink(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" {
		return "", false
	}
	base, _ := url.Parse(ConfabURL)
	if u.Host != base.Host || (u.Path != "" && u.Path != "/") || !callPhrase.MatchString(u.Fragment) {
		return "", false
	}
	return ConfabURL + "/#" + u.Fragment, true
}

// Call starts a confab call: the link to open here, which hosts it, and the
// link to give anyone else.
func (d *Desk) Call() (host, join string, err error) {
	p := phrase.New()
	return ConfabURL + "/#host/" + p, ConfabURL + "/#" + p, nil
}

// CallPeer starts a call and invites a paired device to it. The returned
// link hosts the call and should be opened here.
func (d *Desk) CallPeer(ctx context.Context, peerID string) (string, error) {
	p, err := d.peer(peerID)
	if err != nil {
		return "", err
	}
	host, join, err := d.Call()
	if err != nil {
		return "", err
	}
	data := []byte(join)
	it := xfer.ItemFor(callItem, manifest.KindText, "text/uri-list", data)
	if _, err := d.shareVia(ctx, &p, "call", manifest.Manifest{Items: []manifest.Item{it}}, xfer.Bytes{data}, nil); err != nil {
		return "", err
	}
	return host, nil
}

// isCall reports whether an offer presents itself as a call invite.
func isCall(s xfer.Summary) bool {
	return s.Items == 1 && s.Kind == manifest.KindText && s.Name == callItem && s.Bytes <= 256
}

// ErrBadInvite is reported when a call invite's content is not a confab link.
var ErrBadInvite = errors.New("that call invite was not a confab link, so it was not opened")

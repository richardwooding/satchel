package main

import (
	"fmt"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/richardwooding/satchel/internal/desk"
)

// Notification categories and their buttons. On GNOME the buttons show in
// the banner and in the notification list; clicking the body is
// DEFAULT_ACTION, which opens the window.
const (
	catReceived   = "received"
	catOffer      = "offer"
	actShowFolder = "show-folder"
	actAccept     = "accept"
	actDecline    = "decline"
)

func (s *Service) setupNotifications() {
	for _, c := range []notifications.NotificationCategory{
		{ID: catReceived, Actions: []notifications.NotificationAction{{ID: actShowFolder, Title: "Show in folder"}}},
		{ID: catOffer, Actions: []notifications.NotificationAction{{ID: actAccept, Title: "Accept"}, {ID: actDecline, Title: "Decline"}}},
	} {
		if err := s.notes.RegisterNotificationCategory(c); err != nil {
			log.Printf("notification category %s: %v", c.ID, err)
		}
	}
	s.notes.OnNotificationResponse(func(r notifications.NotificationResult) {
		if r.Error != nil {
			return
		}
		switch r.Response.ActionIdentifier {
		case actShowFolder:
			_ = s.ShowFolder()
		case actAccept, actDecline:
			s.answerOffer(r.Response.ActionIdentifier == actAccept, r.Response.UserInfo)
		default:
			s.showWindow("")
		}
	})
}

// notify is desk's Notify hook.
func (s *Service) notify(n desk.Note) {
	opts := notifications.NotificationOptions{
		ID:    fmt.Sprintf("%s-%d", n.Kind, time.Now().UnixNano()),
		Title: n.Title,
		Body:  n.Body,
	}
	var err error
	switch n.Kind {
	case "received":
		if n.Folder != "" {
			opts.CategoryID = catReceived
			err = s.notes.SendNotificationWithActions(opts)
			break
		}
		err = s.notes.SendNotification(opts)
	case "offer":
		opts.CategoryID = catOffer
		opts.Data = map[string]any{"phrase": n.Phrase, "from": float64(n.From), "offer": n.OfferID}
		err = s.notes.SendNotificationWithActions(opts)
	case "share-ready":
		// The link is already on the clipboard; say so.
		opts.Body += " · link copied"
		err = s.notes.SendNotification(opts)
	default:
		err = s.notes.SendNotification(opts)
	}
	if err != nil {
		log.Printf("notification: %v", err)
	}
}

// answerOffer acts on an offer notification's button. The data rode in the
// notification itself; numbers come back as float64 from its JSON.
func (s *Service) answerOffer(accept bool, info map[string]any) {
	phrase, _ := info["phrase"].(string)
	id, _ := info["offer"].(string)
	from, _ := info["from"].(float64)
	if accept {
		if err := s.Accept(phrase, uint32(from), id); err != nil {
			s.notify(desk.Note{Kind: "failed", Title: "Couldn't accept", Body: err.Error()})
		}
		return
	}
	s.Decline(phrase, uint32(from), id)
}

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
	actShowFolder = "show-folder"
)

func (s *Service) setupNotifications() {
	err := s.notes.RegisterNotificationCategory(notifications.NotificationCategory{
		ID: catReceived, Actions: []notifications.NotificationAction{{ID: actShowFolder, Title: "Show in folder"}},
	})
	if err != nil {
		log.Printf("notification category: %v", err)
	}
	s.notes.OnNotificationResponse(func(r notifications.NotificationResult) {
		if r.Error != nil {
			return
		}
		switch r.Response.ActionIdentifier {
		case actShowFolder:
			_ = s.ShowFolder()
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

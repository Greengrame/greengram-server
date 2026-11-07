// Package ids maps Greengram user ids to what clients see.
package ids

import "sync/atomic"

// Users keep their Greengram id, except the few ids the clients treat as
// Telegram's own accounts (service notifications, support, replies...). A
// user who would land on one is moved up by ReservedShift. The instance's
// announcements account takes 777000 on purpose (phase 7), so the client
// shows it as service notifications.
const (
	ServiceNotifications = 777000
	ReservedShift        = 10_000_000_000
)

var reserved = map[int64]bool{
	777000: true, 333000: true, 42777: true, // service, support
	708513: true, 1271266957: true, // replies
	489000: true, 489001: true, // login codes, oauth
	2666000: true, 136817688: true, // anonymous admin, channel bot
	429000: true, 4240000: true, 4244000: true,
}

// announcements is the instance's announcements account, set at start.
var announcements atomic.Int64

// SetAnnouncements makes id show up as ServiceNotifications everywhere.
func SetAnnouncements(id int64) { announcements.Store(id) }

// Announcements is the account behind ServiceNotifications, 0 if none.
func Announcements() int64 { return announcements.Load() }

// Client is the id a client sees for user id.
func Client(id int64) int64 {
	if a := announcements.Load(); a != 0 && id == a {
		return ServiceNotifications
	}
	if reserved[id] {
		return id + ReservedShift
	}
	return id
}

// Server undoes Client.
func Server(id int64) int64 {
	if id == ServiceNotifications {
		if a := announcements.Load(); a != 0 {
			return a
		}
	}
	if id >= ReservedShift && reserved[id-ReservedShift] {
		return id - ReservedShift
	}
	return id
}

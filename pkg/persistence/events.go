package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/cds-snc/covid-alert-server/pkg/keyclaim"
)

var originatorLookup keyclaim.Authenticator

// InitLookup Setup the originator lookup used to map events to bearerTokens
func SetupLookup(lookup keyclaim.Authenticator) {
	originatorLookup = lookup
}

// Event the event that we are to log
type Event struct {
	Identifier EventType
	DeviceType DeviceType
	Date       time.Time
	Count      int
	Originator string
}

func translateToken(token string) string {
	region, ok := originatorLookup.Authenticate(token)

	// If we forgot to map a token to a PT just return the token
	if region == "302" {
		return token
	}

	// If it's an old token or unknown just return the token
	if ok == false {
		return token
	}

	return region
}

func translateTokenForLogs(token string) string {
	region, ok := originatorLookup.Authenticate(token)

	if region == "302" || ok == false {
		return fmt.Sprintf("%v...%v", token[0:1], token[len(token)-1:len(token)])
	}

	return region
}

// LogEvent Log a failed event
func LogEvent(ctx context.Context, err error, event Event) {

	log(ctx, err).WithFields(logrus.Fields{
		"Originator": translateTokenForLogs(event.Originator),
		"DeviceType": event.DeviceType,
		"Identifier": event.Identifier,
		"Date":       event.Date,
		"Count":      event.Count,
	}).Warn("Unable to log event")
}

// SaveEvent log an Event in the database
func (c *conn) SaveEvent(event Event) error {

	if err := saveEvent(c.db, event); err != nil {
		return err
	}
	return nil
}

// DeviceType the type of the device the event was generated by
type DeviceType string

// EventType the type of the event that happened
type EventType string

// Android events generated by Server
// IOS events generated by iPhones
// Server eveents generated by Server
const (
	Android DeviceType = "Android"
	IOS     DeviceType = "iOS"
	Server  DeviceType = "Server"
)

// OTKClaimed One Time Key Claimed
// OTKGenerated One Time Key Generated
// OTKExpired One Time Key Expired
const (
	OTKClaimed   EventType = "OTKClaimed"
	OTKGenerated EventType = "OTKGenerated"
	OTKExpired   EventType = "OTKExpired"
)

// IsValid validates the Device Type against a list of allowed strings
func (dt DeviceType) IsValid() error {
	switch dt {
	case Android, IOS, Server:
		{
			return nil
		}
	}
	return fmt.Errorf("invalid Device Type: (%s)", dt)
}

// IsValid validates the Event Type against a list of allowed strings
func (et EventType) IsValid() error {
	switch et {
	case OTKGenerated, OTKClaimed, OTKExpired:
		return nil
	}
	return fmt.Errorf("invalid EventType: (%s)", et)
}

func saveEvent(db *sql.DB, e Event) error {
	if err := e.DeviceType.IsValid(); err != nil {
		return err
	}

	if err := e.Identifier.IsValid(); err != nil {
		return err
	}

	originator := translateToken(e.Originator)

	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT INTO events
		(source, identifier, device_type, date, count)
		VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE count = count + ?`,
		originator, e.Identifier, e.DeviceType, e.Date.Format("2006-01-02"), e.Count, e.Count); err != nil {

		if err := tx.Rollback(); err != nil {
			return err
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return nil
}

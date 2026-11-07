// ggdev is a small client for poking at a running server: sign up or in,
// send a message, print history. Not for production use.
//
//	ggdev -http http://127.0.0.1:8092 -user friend -password 'xxxxxxxxxx' -signup -to alice -send "hi"
//	ggdev -http http://127.0.0.1:8092 -user friend -password 'xxxxxxxxxx' -to alice -history
package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"
)

func main() {
	base := flag.String("http", "http://127.0.0.1:8092", "server HTTP address")
	user := flag.String("user", "", "username")
	pass := flag.String("password", "", "password")
	signup := flag.Bool("signup", false, "create the account first")
	to := flag.String("to", "", "username to talk to")
	send := flag.String("send", "", "text to send")
	hist := flag.Bool("history", false, "print the dialog with -to")
	flag.Parse()
	if err := run(*base, *user, *pass, *signup, *to, *send, *hist); err != nil {
		log.Fatal(err)
	}
}

func run(base, user, pass string, signup bool, to, send string, hist bool) error {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	post := func(path string, body any) (map[string]any, error) {
		raw, _ := json.Marshal(body)
		resp, err := hc.Post(base+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		out := map[string]any{}
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: %d %v", path, resp.StatusCode, out["error"])
		}
		return out, nil
	}
	if signup {
		if _, err := post("/api/register", map[string]string{"username": user, "name": user, "password": pass}); err != nil {
			return err
		}
	}
	me, err := post("/api/login", map[string]string{"username": user, "password": pass})
	if err != nil {
		return err
	}
	u, _ := url.Parse(base)
	var token string
	for _, c := range jar.Cookies(u) {
		if c.Name == "session" {
			token = c.Value
		}
	}
	// discovery gives the MTProto address and key, like the Android app
	resp, err := hc.Get(base + "/.well-known/greengram-gateway.json")
	if err != nil {
		return err
	}
	var disc struct {
		DC        int    `json:"dc"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		PublicKey string `json:"publicKey"`
	}
	err = json.NewDecoder(resp.Body).Decode(&disc)
	resp.Body.Close()
	if err != nil {
		return err
	}
	block, _ := pem.Decode([]byte(disc.PublicKey))
	if block == nil {
		return errors.New("discovery: bad public key")
	}
	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return err
	}
	return talk(disc.DC, disc.Host, disc.Port, key, int64(me["id"].(float64)), token, to, send, hist)
}

func talk(dc int, host string, port int, key *rsa.PublicKey, uid int64, token, to, send string, hist bool) error {
	c := telegram.NewClient(1, "ggdev", telegram.Options{
		PublicKeys:     []telegram.PublicKey{{RSA: key}},
		DC:             dc,
		DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: host, Port: port}}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		NoUpdates:      true,
		SessionStorage: &session.StorageMemory{},
		Device:         telegram.DeviceConfig{DeviceModel: "ggdev"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return c.Run(ctx, func(ctx context.Context) error {
		api := c.API()
		if _, err := api.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: uid, Bytes: []byte(token)}); err != nil {
			return err
		}
		if to == "" {
			return nil
		}
		found, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: to})
		if err != nil {
			return err
		}
		peer := &tg.InputPeerUser{UserID: found.Peer.(*tg.PeerUser).UserID}
		if send != "" {
			if _, err := api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peer, Message: send, RandomID: rand.Int64()}); err != nil {
				return err
			}
			fmt.Println("sent")
		}
		if hist {
			h, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 20})
			if err != nil {
				return err
			}
			for _, m := range h.(*tg.MessagesMessagesSlice).Messages {
				msg := m.(*tg.Message)
				who := "them"
				if msg.Out {
					who = "me"
				}
				fmt.Printf("%d %s %s: %s\n", msg.ID, time.Unix(int64(msg.Date), 0).Format("15:04:05"), who, msg.Message)
			}
		}
		return nil
	})
}

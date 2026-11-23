// Package mongodb is anvil's MongoDB backend (SPEC §2.3): connection,
// databases and collections, document pages for the grid, editing by _id
// and database commands from the editor. It speaks the same types as the
// SQL packages (drivers.Table/Column, query.Page, editing.Change) so the
// UI reuses its grid, editing and export.
package mongodb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/Watts-Argentina/anvil/internal/connections"
	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// URI is the connection string: the host itself if it is one, or
// mongodb://host:port/.
func URI(c models.DBConn) string {
	if connections.IsMongoURI(c.Host) {
		return c.Host
	}
	return "mongodb://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) + "/"
}

// Connect opens the client and checks it with a ping.
func Connect(ctx context.Context, c models.DBConn, password string) (*mongo.Client, error) {
	opts := options.Client().ApplyURI(URI(c)).SetServerSelectionTimeout(drivers.TestTimeout).SetAppName("anvil")
	if c.User != "" {
		// Keep what the URI says (authSource, mechanism).
		cred := options.Credential{}
		if opts.Auth != nil {
			cred = *opts.Auth
		}
		cred.Username, cred.Password = c.User, password
		opts.SetAuth(cred)
	}
	if cfg, err := tlsConfig(c); err != nil {
		return nil, connectError(err)
	} else if cfg != nil {
		opts.SetTLSConfig(cfg)
	}

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, connectError(err)
	}
	ctx, cancel := context.WithTimeout(ctx, drivers.TestTimeout)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(context.Background())
		return nil, connectError(err)
	}
	return client, nil
}

// Test connects, pings and disconnects (SPEC §3.3).
func Test(ctx context.Context, c models.DBConn, password string) error {
	client, err := Connect(ctx, c, password)
	if err != nil {
		return err
	}
	return client.Disconnect(ctx)
}

// tlsConfig maps the SSL mode: "require" encrypts without verifying the
// certificate, "verify-full" verifies it (with SSLCA if set). Both send the
// client certificate if there is one. nil means the URI decides.
func tlsConfig(c models.DBConn) (*tls.Config, error) {
	var cfg *tls.Config
	switch c.SSLMode {
	case "require":
		cfg = &tls.Config{InsecureSkipVerify: true}
	case "verify-full":
		cfg = &tls.Config{}
	default:
		return nil, nil
	}
	if c.SSLCA != "" {
		pem, err := os.ReadFile(c.SSLCA)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates in %s", c.SSLCA)
		}
		cfg.RootCAs = pool
	}
	if c.SSLCert != "" {
		key := c.SSLKey
		if key == "" {
			key = c.SSLCert // MongoDB usually ships the key in the same PEM
		}
		cert, err := tls.LoadX509KeyPair(c.SSLCert, key)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func connectError(err error) error {
	return appErrors.Error(
		"Failed to connect.",
		"Check the connection settings and that the server is reachable: "+err.Error(),
		err,
	)
}

// Transactions reports whether the server supports multi-document
// transactions: replica set members and mongos do, standalone servers do not.
func Transactions(ctx context.Context, client *mongo.Client) bool {
	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
	return err == nil && (hello.SetName != "" || hello.Msg == "isdbgrid")
}

package main

import (
	"context"
	"flag"
	"fmt"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// runVapidKeys prints a new pair of VAPID keys in the form of a configuration
// file, so that the operator can put them in the settings of Web Push.
func runVapidKeys(_ context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("freshgo vapid-keys", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage: freshgo vapid-keys\n\n"+
			"Prints a new pair of keys that sign Web Push messages, as lines of the file -config names:\n"+
			"vapid-public-key and vapid-private-key. The same values go in -vapid-public-key,\n"+
			"-vapid-private-key, $FRESHGO_VAPID_PUBLIC_KEY and $FRESHGO_VAPID_PRIVATE_KEY. Push is on\n"+
			"when -vapid-subject, a mailto: or https: address push services may contact you at, is set too.\n"+
			"A new pair makes every browser subscribed with the old one stop getting messages.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "vapid-public-key: %s\nvapid-private-key: %s\n", public, private)
	return err
}

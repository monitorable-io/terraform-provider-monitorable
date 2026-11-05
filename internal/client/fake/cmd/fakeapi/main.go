// Command fakeapi starts the in-process API fake and blocks until interrupted.
//
// It exists for manual checks of a locally built provider, for example the
// release dry run: start it, point the provider at the printed URL with
// MONITORABLE_ENDPOINT, use any key starting with "mon_test_fake" as
// MONITORABLE_API_KEY, then stop it with Ctrl-C.
//
//	go run ./internal/client/fake/cmd/fakeapi
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

func main() {
	f := fake.New(fake.Options{})
	defer f.Close()

	fmt.Println(f.URL())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

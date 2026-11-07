// loopforge-account explicitly provisions a local account. It never runs on
// service startup. Read the password from stdin so it does not enter argv.
package main

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

func main() {
	login := flag.String("user", "", "account login")
	admin := flag.Bool("admin", false, "grant platform administration")
	flag.Parse()
	if *login == "" || len(*login) > 64 || strings.ContainsAny(*login, " \t\r\n") {
		log.Fatal("a login of 1–64 characters without spaces is required")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 74))
	if err != nil {
		log.Fatal(err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(password) < repo.MinPasswordLength || len(password) > repo.MaxPasswordLength {
		log.Fatalf("password must contain %d–%d bytes on stdin", repo.MinPasswordLength, repo.MaxPasswordLength)
	}
	s, err := repo.Open("")
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err := s.PutUser(domain.User{LoginName: *login, ChineseName: *login}); err != nil {
		log.Fatal(err)
	}
	u, ok := s.UserByLoginName(*login)
	if !ok {
		log.Fatal("could not read account")
	}
	if err := s.SetLocalPassword(u.ID, password); err != nil {
		log.Fatal(err)
	}
	if *admin {
		if err := s.SetUserPlatformRole(u.ID, domain.PlatformRoleAdmin); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("Account %s is ready.\n", *login)
}

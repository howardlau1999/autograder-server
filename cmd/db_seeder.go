package main

import (
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"context"
	"github.com/cockroachdb/pebble"
	"golang.org/x/crypto/bcrypt"
	"log"
)

func main() {
	db, err := pebble.Open("db", &pebble.Options{Merger: repository.NewKVMerger()})
	if err != nil {
		panic(err)
	}
	userRepo := repository.NewKVUserRepository(db)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("root"), bcrypt.DefaultCost)
	rootUser := &model_pb.User{Username: "root", Password: passwordHash, Email: "bob@example.com"}
	id, err := userRepo.CreateUser(context.Background(), rootUser)
	if err != nil {
		panic(err)
	}
	log.Printf("Created root user. uid = %d", id)
	iter := db.NewIter(repository.PrefixIterOptions([]byte("user:")))
	for iter.First(); iter.Valid(); iter.Next() {
		log.Printf("%s", string(iter.Key()))
	}
	if err := iter.Close(); err != nil {
		panic(err)
	}
	db.Close()
}

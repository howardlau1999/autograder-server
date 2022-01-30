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
	courseRepo := repository.NewKVCourseRepository(db)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("root"), bcrypt.DefaultCost)
	rootUser := &model_pb.User{Username: "root", Password: passwordHash, Email: "bob@example.com"}
	id, err := userRepo.CreateUser(context.Background(), rootUser)
	if err != nil {
		panic(err)
	}
	log.Printf("Created root user. uid = %d", id)
	firstCourse := &model_pb.Course{
		Name:        "你好，世界！",
		ShortName:   "HLW 101",
		Term:        "2022",
		Description: "这是第一个课程。",
	}
	courseId, err := courseRepo.CreateCourse(context.Background(), firstCourse)
	if err != nil {
		panic(err)
	}

	member := &model_pb.CourseMember{
		UserId:   id,
		Role:     model_pb.CourseRole_Instructor,
		CourseId: courseId,
	}
	err = userRepo.AddToCourse(context.Background(), member)
	if err != nil {
		panic(err)
	}
	err = courseRepo.AddUser(context.Background(), member)
	if err != nil {
		panic(err)
	}
	courses, err := userRepo.GetCoursesByUser(context.Background(), id)
	if err != nil {
		panic(err)
	}
	members, err := courseRepo.GetUsersByCourse(context.Background(), courseId)
	if err != nil {
		panic(err)
	}
	log.Printf("User courses: %v", courses)
	log.Printf("Course members: %v", members)
	iter := db.NewIter(repository.PrefixIterOptions([]byte("")))
	for iter.First(); iter.Valid(); iter.Next() {
		log.Printf("%s", string(iter.Key()))
	}
	if err := iter.Close(); err != nil {
		panic(err)
	}
	db.Close()
}

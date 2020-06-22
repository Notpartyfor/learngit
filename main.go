package main

import (
	"context"
	hello "git.tencent.com/cloud_industry/examples/greeter/srv/proto/hello"
	"github.com/micro/go-micro"
	"github.com/micro/go-micro/server/grpc"
	"log"
)

type Say struct{}

func (s *Say) Hello(ctx context.Context, req *hello.Request, rsp *hello.Response) error {
	log.Print("Received Say.Hello request")
	rsp.Msg = "Hello " +
		"	Name : " + req.Name +
		"	UserName : " + req.User.Name +
		"	User1Name : " + req.GetUsers()[1].Name +
		"	User1Job : " + req.GetUsers()[1].Jobs[1].Name
	return nil
}

func (s *Say) Hello2(ctx context.Context, req *hello.Request2, rsp *hello.Response) error {
	log.Print("Received Say.Hello2 request")
	//rsp.Msg = "OK"
	rsp.Msg = "Hello2 "
	return nil
}

func main() {
	service := micro.NewService(
		micro.Server(grpc.NewServer()),
		micro.Name("go.micro.srv.greeter"),
	)
	// optionally setup command line usage
	service.Init()

	// Register Handlers
	if err := hello.RegisterSayHandler(service.Server(), new(Say)); err != nil {
		log.Fatal(err)
	}

	// Run server
	if err := service.Run(); err != nil {
		log.Fatal(err)
	}
}

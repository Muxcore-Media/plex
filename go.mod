module github.com/Muxcore-Media/plex

go 1.26.4

require (
	github.com/Muxcore-Media/core/pkg/contracts v0.5.8
	github.com/Muxcore-Media/core/sdk/go/client v0.5.8
	github.com/Muxcore-Media/core/sdk/go/module v0.5.8
	github.com/Muxcore-Media/contracts-playback v0.1.0
	github.com/Muxcore-Media/playback-contract v0.1.0
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/Muxcore-Media/core v0.5.8 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260610212136-7ab31c22f7ad // indirect
)





replace github.com/Muxcore-Media/contracts-playback => ../contracts-playback

replace github.com/Muxcore-Media/playback-contract => ../playback-contract

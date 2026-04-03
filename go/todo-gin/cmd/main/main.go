package main

import (
	"context"
	"log"
	"net/http"
	"os"

	_ "net/http/pprof"

	"github.com/gin-gonic/gin"
	"github.com/hardikchoksi151/todo-gin/config"
	"github.com/hardikchoksi151/todo-gin/internal/auth"
	"github.com/hardikchoksi151/todo-gin/internal/todo"
	"github.com/hardikchoksi151/todo-gin/internal/user"
	"github.com/hardikchoksi151/todo-gin/pkg/db"
	track "github.com/middleware-labs/golang-apm/tracker"
	"github.com/urfave/cli"
)

func main() {

	tracer, err := track.Track(
		track.WithConfigTag("service", "YOUR_SERVICE_NAME"),
		track.WithConfigTag("accessToken", "<MW_API_KEY>"), // optional in Host mode
	)
	if err != nil {
		log.Fatalf("Failed to initialize middleware: %v", err)
	}
	defer func() { _ = tracer.Tp.Shutdown(context.Background()) }()

	app := &cli.App{
		Name:  "todo=app",
		Usage: "A simple todo app with user auth",
		Commands: []cli.Command{
			{
				Name:  "migrate",
				Usage: "Run database migrations",
				Action: func(c *cli.Context) error {
					cfg := config.LoadConfig()
					database := db.ConnectDB(cfg)
					db.RunMigrations(database)
					return nil
				},
			},
			{
				Name:  "serve",
				Usage: "Start the server",
				Action: func(c *cli.Context) error {
					startServer()
					return nil
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}

}

func startServer() {
	cfg := config.LoadConfig()
	database := db.ConnectDB(cfg)

	userRepo := user.NewUserStore(database)
	userService := user.NewUserService(userRepo)
	userHandler := user.NewHandler(userService, *cfg)

	todoRepo := todo.NewTodoRepo(database)
	todoService := todo.NewTodoService(todoRepo)
	todoHandler := todo.NewHandler(todoService)

	r := gin.Default()

	authMiddleware := auth.Middleware(cfg.JWTSecret)

	r.POST("/register", userHandler.Register)
	r.POST("/login", userHandler.Login)

	authorized := r.Group("/")
	authorized.Use(authMiddleware)
	{
		authorized.POST("/todos", todoHandler.CreateTodo)
		authorized.GET("/todos", todoHandler.GetTodos)
	}

	// Start pprof server on separate port
	go func() {
		log.Println("Starting pprof server on :6060")
		if err := http.ListenAndServe(":6060", nil); err != nil {
			log.Printf("pprof server error: %v", err)
		}
	}()

	if err := r.Run(":8080"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

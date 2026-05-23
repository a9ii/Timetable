// =============================================================================
// FILE: main.go
// PACKAGE: main
//
// PURPOSE:
//   Bootstraps the Fiber v2 HTTP server, registers all REST API endpoints for
//   CRUD operations on master data, and implements the async solver pipeline:
//
//     POST /api/generate  →  spawns Goroutine  →  returns 202 + JobID
//     GET  /ws/:jobId     →  WebSocket, streams ProgressEvents until closed
//
// ARCHITECTURE NOTES:
//   - A global sync.Map (jobChannels) maps JobID → ProgressEvent channel.
//     The WebSocket handler looks up the channel by JobID and reads from it.
//   - Fiber's built-in WebSocket middleware is used instead of a raw
//     gorilla/websocket import to stay in the Fiber ecosystem.
//   - All CORS headers are set permissively for local development; tighten
//     the AllowOrigins list before production deployment.
//   - Static files (index.html, app.js) are served from the ./static directory.
// =============================================================================
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/websocket/v2"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

// =============================================================================
// GLOBAL STATE
// =============================================================================

// jobChannels maps a JobID (string UUID) to its progress channel.
// sync.Map is used for lock-free concurrent reads/writes from multiple goroutines
// (solver goroutine writes, WebSocket goroutine reads).
var jobChannels sync.Map

// =============================================================================
// APPLICATION ENTRY POINT
// =============================================================================

func main() {
	// Load .env file if present (ignored in production where env vars are set directly).
	_ = godotenv.Load()

	// Initialise database connection and run AutoMigrate.
	db := InitDB()

	// Populate demo data if the database is empty.
	SeedDemoData(db)

	// Create the Fiber application with custom config.
	app := fiber.New(fiber.Config{
		// Return structured JSON errors instead of HTML error pages.
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"error":   true,
				"message": err.Error(),
			})
		},
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	})

	// --- Global Middleware ---
	app.Use(recover.New())   // Recover from panics, prevent server crash.
	app.Use(logger.New())    // Structured request logging to stdout.
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*", // Tighten in production!
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
	}))

	// --- Static File Serving ---
	// Serve the frontend from the ./static directory.
	app.Static("/", "./static", fiber.Static{
		Browse:    false,
		Index:     "index.html",
		MaxAge:    0, // No caching in development.
		Compress:  true,
	})

	// --- WebSocket Upgrade Middleware ---
	// Fiber requires an upgrade check before the actual WS handler.
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			c.Locals("allowed", true)
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	// ==========================================================================
	// API ROUTES
	// ==========================================================================

	api := app.Group("/api")

	// --- Teacher CRUD ---
	api.Get("/teachers", makeListHandler(db, &[]Teacher{}))
	api.Post("/teachers", makeCreateHandler(db, func() interface{} { return &Teacher{} }))
	api.Put("/teachers/:id", makeUpdateHandler(db, func() interface{} { return &Teacher{} }))
	api.Delete("/teachers/:id", makeDeleteHandler(db, &Teacher{}))

	// --- Class CRUD ---
	api.Get("/classes", makeListHandler(db, &[]Class{}))
	api.Post("/classes", makeCreateHandler(db, func() interface{} { return &Class{} }))
	api.Put("/classes/:id", makeUpdateHandler(db, func() interface{} { return &Class{} }))
	api.Delete("/classes/:id", makeDeleteHandler(db, &Class{}))

	// --- Subject CRUD ---
	api.Get("/subjects", makeListHandler(db, &[]Subject{}))
	api.Post("/subjects", makeCreateHandler(db, func() interface{} { return &Subject{} }))
	api.Put("/subjects/:id", makeUpdateHandler(db, func() interface{} { return &Subject{} }))
	api.Delete("/subjects/:id", makeDeleteHandler(db, &Subject{}))

	// --- Classroom CRUD ---
	api.Get("/classrooms", makeListHandler(db, &[]Classroom{}))
	api.Post("/classrooms", makeCreateHandler(db, func() interface{} { return &Classroom{} }))
	api.Put("/classrooms/:id", makeUpdateHandler(db, func() interface{} { return &Classroom{} }))
	api.Delete("/classrooms/:id", makeDeleteHandler(db, &Classroom{}))

	// --- Period CRUD ---
	api.Get("/periods", func(c *fiber.Ctx) error {
		var items []Period
		if err := db.Order("\"index\" ASC").Find(&items).Error; err != nil {
			return err
		}
		return c.JSON(items)
	})
	api.Post("/periods", makeCreateHandler(db, func() interface{} { return &Period{} }))
	api.Put("/periods/:id", makeUpdateHandler(db, func() interface{} { return &Period{} }))
	api.Delete("/periods/:id", makeDeleteHandler(db, &Period{}))

	// --- DayDef CRUD ---
	api.Get("/days", func(c *fiber.Ctx) error {
		var items []DayDef
		if err := db.Order("\"index\" ASC").Find(&items).Error; err != nil {
			return err
		}
		return c.JSON(items)
	})
	api.Post("/days", makeCreateHandler(db, func() interface{} { return &DayDef{} }))
	api.Put("/days/:id", makeUpdateHandler(db, func() interface{} { return &DayDef{} }))
	api.Delete("/days/:id", makeDeleteHandler(db, &DayDef{}))

	// --- Lesson CRUD ---
	api.Get("/lessons", func(c *fiber.Ctx) error {
		var items []Lesson
		if err := db.Preload("Subject").Find(&items).Error; err != nil {
			return err
		}
		return c.JSON(items)
	})
	api.Post("/lessons", makeCreateHandler(db, func() interface{} { return &Lesson{} }))
	api.Put("/lessons/:id", makeUpdateHandler(db, func() interface{} { return &Lesson{} }))
	api.Delete("/lessons/:id", makeDeleteHandler(db, &Lesson{}))

	// --- Card READ + PATCH (move) ---
	api.Get("/cards", func(c *fiber.Ctx) error {
		// Load all cards with full associations.
		var items []Card
		if err := db.Preload("Lesson").Preload("Lesson.Subject").Find(&items).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		// Optional: filter by classId in Go memory.
		// This avoids complex JOIN+ANY() SQL which fails with pgBouncer's
		// simple query protocol and GORM's unpredictable array column naming.
		if rawID := c.Query("classId"); rawID != "" {
			filterUID, err := uuid.Parse(rawID)
			if err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "invalid classId UUID")
			}
			filtered := items[:0] // reuse underlying array, no extra alloc
			for _, card := range items {
				for _, cid := range card.Lesson.ClassIDs {
					if cid == filterUID {
						filtered = append(filtered, card)
						break
					}
				}
			}
			items = filtered
		}

		return c.JSON(items)
	})

	// PATCH /api/cards/:id — move a card to a new (day, period) slot.
	// Enforces all three hard constraints before committing the move.
	api.Patch("/cards/:id", func(c *fiber.Ctx) error {
		cardID := c.Params("id")

		// Parse move request body
		var req struct {
			PeriodID string `json:"periodId"`
			DayDefID string `json:"dayDefId"`
		}
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid request body")
		}
		if req.PeriodID == "" || req.DayDefID == "" {
			return fiber.NewError(fiber.StatusBadRequest, "periodId and dayDefId are required")
		}

		// Load the card being moved (with its lesson + teachers/classes)
		var movingCard Card
		if err := db.Preload("Lesson").First(&movingCard, "id = ?", cardID).Error; err != nil {
			return fiber.NewError(fiber.StatusNotFound, "Card not found")
		}

		// If the destination is the same as the current slot, nothing to do
		if movingCard.PeriodID == req.PeriodID && movingCard.DayDefID == req.DayDefID {
			return c.JSON(movingCard)
		}

		// Load ALL cards already occupying the target slot (excluding the card being moved)
		var slotCards []Card
		if err := db.Preload("Lesson").
			Where("period_id = ? AND day_def_id = ? AND id != ?",
				req.PeriodID, req.DayDefID, cardID).
			Find(&slotCards).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		// Verify teacher availability constraint for the target day
		var movingTeacherEntities []Teacher
		if err := db.Where("id IN ?", []uuid.UUID(movingCard.Lesson.TeacherIDs)).Find(&movingTeacherEntities).Error; err == nil {
			for _, tEntity := range movingTeacherEntities {
				if len(tEntity.AvailableDayIDs) > 0 {
					dayAllowed := false
					for _, allowedDay := range tEntity.AvailableDayIDs {
						if allowedDay == req.DayDefID {
							dayAllowed = true
							break
						}
					}
					if !dayAllowed {
						return c.Status(fiber.StatusConflict).JSON(fiber.Map{
							"error":   true,
							"message": fmt.Sprintf("⚠️ Teacher %s is not available on day %s.", tEntity.Name, req.DayDefID),
						})
					}
				}
			}
		}

		// Verify Shift constraint for the moving classes vs target period
		var targetPeriod Period
		if err := db.First(&targetPeriod, "id = ?", req.PeriodID).Error; err == nil {
			var movingClassEntities []Class
			if err := db.Where("id IN ?", []uuid.UUID(movingCard.Lesson.ClassIDs)).Find(&movingClassEntities).Error; err == nil {
				for _, cEntity := range movingClassEntities {
					if cEntity.Shift != "" && targetPeriod.Shift != "" && cEntity.Shift != targetPeriod.Shift {
						return c.Status(fiber.StatusConflict).JSON(fiber.Map{
							"error":   true,
							"message": fmt.Sprintf("⚠️ Class %s is shift '%s' but period is '%s'.", cEntity.Name, cEntity.Shift, targetPeriod.Shift),
						})
					}
				}
			}
		}

		// Build lookup sets for the moving lesson's teachers and classes
		movingTeachers := make(map[uuid.UUID]bool)
		for _, tid := range movingCard.Lesson.TeacherIDs {
			movingTeachers[tid] = true
		}
		movingClasses := make(map[uuid.UUID]bool)
		for _, cid := range movingCard.Lesson.ClassIDs {
			movingClasses[cid] = true
		}

		// Track which rooms are occupied in the target slot
		occupiedRooms := make(map[uuid.UUID]bool)

		// Check each existing card in the target slot for conflicts
		for _, sc := range slotCards {
			// Teacher conflict
			for _, tid := range sc.Lesson.TeacherIDs {
				if movingTeachers[tid] {
					var teacher Teacher
					db.First(&teacher, "id = ?", tid)
					
					var classNames []string
					for _, cid := range sc.Lesson.ClassIDs {
						var class Class
						if db.First(&class, "id = ?", cid).Error == nil {
							classNames = append(classNames, class.Name)
						}
					}
					subjectName := "a subject"
					if sc.Lesson.Subject.Name != "" {
						subjectName = sc.Lesson.Subject.Name
					}

					msg := fmt.Sprintf("⚠️ Teacher conflict: %s is already teaching %s (%s) in this slot.", teacher.Name, subjectName, strings.Join(classNames, ", "))
					return c.Status(fiber.StatusConflict).JSON(fiber.Map{
						"error":   true,
						"message": msg,
					})
				}
			}
			// Class conflict
			for _, cid := range sc.Lesson.ClassIDs {
				if movingClasses[cid] {
					var class Class
					db.First(&class, "id = ?", cid)
					
					subjectName := "a subject"
					if sc.Lesson.Subject.Name != "" {
						subjectName = sc.Lesson.Subject.Name
					}

					msg := fmt.Sprintf("⚠️ Class conflict: %s is already scheduled for %s in this slot.", class.Name, subjectName)
					return c.Status(fiber.StatusConflict).JSON(fiber.Map{
						"error":   true,
						"message": msg,
					})
				}
			}
			// Track occupied rooms for room reassignment below
			if sc.ClassroomID != nil {
				occupiedRooms[*sc.ClassroomID] = true
			}
		}

		// Room reassignment: find a free room that matches the lab requirement
		var classrooms []Classroom
		db.Find(&classrooms)
		needsLab := movingCard.Lesson.Subject.RequiresLab
		var newRoomID *uuid.UUID
		for i := range classrooms {
			r := &classrooms[i]
			if r.IsLab != needsLab {
				continue
			}
			if occupiedRooms[r.ID] {
				continue
			}
			id := r.ID
			newRoomID = &id
			break
		}
		if needsLab && newRoomID == nil {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error":   true,
				"message": "⚠️ Room conflict: no available lab room in that slot.",
			})
		}

		// All constraints satisfied — commit the move
		movingCard.PeriodID    = req.PeriodID
		movingCard.DayDefID    = req.DayDefID
		movingCard.ClassroomID = newRoomID
		if err := db.Save(&movingCard).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}

		// Reload with full associations for the response
		db.Preload("Lesson").Preload("Lesson.Subject").First(&movingCard, "id = ?", cardID)
		return c.JSON(movingCard)
	})

	// --- Solver Trigger ---
	// POST /api/generate
	// Returns 202 Accepted immediately with a JobID.
	// The actual solving happens in a goroutine.
	api.Post("/generate", func(c *fiber.Ctx) error {
		jobID := uuid.New().String()

		// Buffered channel — large enough that the solver never blocks even if
		// the WebSocket consumer falls slightly behind.
		progressCh := make(chan ProgressEvent, 200)
		jobChannels.Store(jobID, progressCh)

		// Spawn the solver in a goroutine.  The HTTP response returns immediately.
		go func() {
			defer func() {
				// Remove the channel from the map once the solver is done (or panics).
				// The channel is already closed by RunSolver's defer.
				time.Sleep(30 * time.Second) // Keep entry alive briefly for slow WS connects.
				jobChannels.Delete(jobID)
			}()
			RunSolver(db, progressCh)
		}()

		log.Printf("[API] Solver job %s spawned.", jobID)

		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"jobId":   jobID,
			"message": "Solver started. Connect to WebSocket for real-time progress.",
		})
	})

	// ==========================================================================
	// WEBSOCKET ROUTE
	// ==========================================================================

	// GET /ws/:jobId
	// Streams ProgressEvent JSON messages until the solver channel is closed.
	app.Get("/ws/:jobId", websocket.New(func(c *websocket.Conn) {
		jobID := c.Params("jobId")
		log.Printf("[WS] Client connected for job %s", jobID)

		// Disable read deadline — the solver may run for many seconds.
		// Without this, Fiber's default deadline kills the connection mid-solve.
		_ = c.SetReadDeadline(time.Time{})
		_ = c.SetWriteDeadline(time.Time{})

		// Poll for the channel (guard against rare race where WS connects
		// before the goroutine has stored the channel).
		var progressCh chan ProgressEvent
		for i := 0; i < 30; i++ {
			if v, ok := jobChannels.Load(jobID); ok {
				progressCh = v.(chan ProgressEvent)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if progressCh == nil {
			errMsg, _ := json.Marshal(ProgressEvent{
				Type:    ProgressFailed,
				Message: fmt.Sprintf("Job %s not found or expired.", jobID),
			})
			_ = c.WriteMessage(websocket.TextMessage, errMsg)
			return
		}

		// Background goroutine: send a WebSocket ping frame every 20 s so
		// browsers and proxies don't close the idle connection.
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(20 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
						return
					}
				}
			}
		}()
		defer close(done)

		// Drain progress channel → forward every event to the browser.
		for event := range progressCh {
			payload, err := json.Marshal(event)
			if err != nil {
				log.Printf("[WS] Marshal error: %v", err)
				continue
			}
			if err := c.WriteMessage(websocket.TextMessage, payload); err != nil {
				log.Printf("[WS] Write error for job %s: %v", jobID, err)
				return
			}
			if event.Type == ProgressCompleted || event.Type == ProgressFailed {
				time.Sleep(200 * time.Millisecond)
				return
			}
		}
		log.Printf("[WS] Job %s channel closed.", jobID)
	}))

	// ==========================================================================
	// START SERVER
	// ==========================================================================

	port := getEnv("PORT", "8080")
	log.Printf("[Server] Listening on http://localhost:%s", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("[Server] Fatal: %v", err)
	}
}

// =============================================================================
// GENERIC CRUD HANDLER FACTORIES
// =============================================================================
// These factories eliminate repetitive boilerplate while preserving full type
// safety and proper error handling for each resource type.

// makeListHandler returns a Fiber handler that fetches all records of type T.
func makeListHandler(db *gorm.DB, dest interface{}) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := db.Find(dest).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		return c.JSON(dest)
	}
}

// makeCreateHandler returns a Fiber handler that parses the request body and
// inserts a new record into the database.
func makeCreateHandler(db *gorm.DB, factory func() interface{}) fiber.Handler {
	return func(c *fiber.Ctx) error {
		record := factory()
		if err := c.BodyParser(record); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid request body: "+err.Error())
		}
		if err := db.Create(record).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		return c.Status(fiber.StatusCreated).JSON(record)
	}
}

// makeUpdateHandler returns a Fiber handler that performs a partial update
// (only the fields provided in the body are changed — GORM's Save/Updates).
func makeUpdateHandler(db *gorm.DB, factory func() interface{}) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		record := factory()
		// First, load the existing record to ensure it exists.
		if err := db.First(record, "id = ?", id).Error; err != nil {
			return fiber.NewError(fiber.StatusNotFound, "Record not found")
		}
		// Parse the update payload over the loaded struct.
		if err := c.BodyParser(record); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "Invalid request body: "+err.Error())
		}
		// Save uses all non-zero fields; for partial update use db.Model(record).Updates(record).
		if err := db.Save(record).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		return c.JSON(record)
	}
}

// makeDeleteHandler returns a Fiber handler that hard-deletes a record by ID.
func makeDeleteHandler(db *gorm.DB, model interface{}) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		result := db.Delete(model, "id = ?", id)
		if result.Error != nil {
			return fiber.NewError(fiber.StatusInternalServerError, result.Error.Error())
		}
		if result.RowsAffected == 0 {
			return fiber.NewError(fiber.StatusNotFound, "Record not found")
		}
		return c.JSON(fiber.Map{"deleted": true, "id": id})
	}
}

// ensureStaticDir makes sure the ./static directory exists so Fiber can serve
// frontend files.  Called implicitly by the OS when you place files there;
// kept here as documentation.
func ensureStaticDir() {
	if err := os.MkdirAll("./static", 0755); err != nil {
		log.Printf("[Static] Warning: could not create static dir: %v", err)
	}
}

func init() {
	ensureStaticDir()
}

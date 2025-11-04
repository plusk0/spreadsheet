package main

import (
	"log"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

func main() {
	// load schema from JSON config (preferred)
	schema, err := loadConfig("./config.json")
	if err != nil {
		log.Fatal("failed to load config:", err)
	}

	log.Printf("loaded schema: %+v", schema)

	// Initialize the application
	a := app.New()

	// Set up the SQLite database
	db := initializeDB()
	defer db.Close()

	// Create the main window
	win := a.NewWindow("Simple Data Management App")

	// Set the content of the window (UI) with schema
	win.SetContent(createUI(win, db, schema))

	// Set a default window size
	win.Resize(fyne.NewSize(900, 640))

	// Show the window and start the application
	win.ShowAndRun()
}

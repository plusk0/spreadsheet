package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

// colResizer is a small draggable widget used to resize columns.
type colResizer struct {
	widget.BaseWidget
	onDrag func(dx float32)
	rect   *canvas.Rectangle
}

func newColResizer(onDrag func(dx float32)) *colResizer {
	r := &colResizer{onDrag: onDrag}
	r.ExtendBaseWidget(r)
	return r
}

func (r *colResizer) CreateRenderer() fyne.WidgetRenderer {
	if r.rect == nil {
		r.rect = canvas.NewRectangle(color.NRGBA{R: 200, G: 200, B: 200, A: 200})
	}
	objs := []fyne.CanvasObject{r.rect}
	return &resizerRenderer{rect: r.rect, objs: objs}
}

func (r *colResizer) Dragged(e *fyne.DragEvent) {
	if r.onDrag != nil {
		// use e.Dragged.DX from DragEvent
		r.onDrag(e.Dragged.DX)
	}
}

func (r *colResizer) DragEnd() {}

type resizerRenderer struct {
	rect *canvas.Rectangle
	objs []fyne.CanvasObject
}

func (rr *resizerRenderer) MinSize() fyne.Size           { return fyne.NewSize(6, 24) }
func (rr *resizerRenderer) Layout(size fyne.Size)        { rr.rect.Resize(size) }
func (rr *resizerRenderer) Refresh()                     { rr.rect.Refresh() }
func (rr *resizerRenderer) Objects() []fyne.CanvasObject { return rr.objs }
func (rr *resizerRenderer) Destroy()                     {}
func (rr *resizerRenderer) BackgroundColor() color.Color { return color.Transparent }

// clickableOverlay is a transparent canvas object that captures both left and right clicks
type clickableOverlay struct {
	canvas.Rectangle
	onLeftClick  func()
	onRightClick func()
}

// Tapped implements the fyne.Tappable interface for left clicks
func (c *clickableOverlay) Tapped(*fyne.PointEvent) {
	if c.onLeftClick != nil {
		c.onLeftClick()
	}
}

// TappedSecondary implements the fyne.TappableSecondary interface for right clicks
func (c *clickableOverlay) TappedSecondary(*fyne.PointEvent) {
	if c.onRightClick != nil {
		c.onRightClick()
	}
}

// newClickableOverlay creates a transparent overlay that can detect clicks
func newClickableOverlay(onLeft func(), onRight func()) *clickableOverlay {
	overlay := &clickableOverlay{
		onLeftClick:  onLeft,
		onRightClick: onRight,
	}
	// Make it fully transparent so the label underneath is visible
	overlay.FillColor = color.Transparent
	overlay.StrokeColor = color.Transparent
	return overlay
}

// mergeWithSchema returns a copy of data overlaid on the defaults from schema.
// This ensures missing keys (e.g. when importing old JSON) are present for UI/export.
func mergeWithSchema(schema []FieldDef, data map[string]interface{}) map[string]interface{} {
	m := getEmptyRowFromSchema(schema)
	if data == nil {
		return m
	}
	for k, v := range data {
		m[k] = v
	}
	return m
}

// createUI builds the whole UI based on schema.
func createUI(win fyne.Window, db *sql.DB, schema []FieldDef) fyne.CanvasObject {
	cols := len(schema) + 1 // +1 for actions column

	// column widths in pixels (float32). Start with a reasonable default.
	colWidths := make([]float32, cols)
	for i := range colWidths {
		colWidths[i] = 160
	}

	// rows container: header + many row containers stacked vertically
	rowsContainer := container.NewVBox()

	// views in memory (loaded from DB)
	var savedViews []View
	loadViews := func() {
		v, err := getAllViews(db)
		if err != nil {
			log.Println("warning: failed to load views:", err)
			savedViews = nil
			return
		}
		savedViews = v
	}
	loadViews()

	// current view state
	currentViewID := 0               // 0 means "All"
	currentViewName := "All"         // label shown
	currentVisibleCols := []string{} // empty means all
	fmt.Printf(currentViewName)

	// helper to set view by id (0 => All)
	setViewByID := func(id int) {
		currentViewID = id
		if id == 0 {
			currentViewName = "All"
			currentVisibleCols = nil
			populateTableGrid(win, rowsContainer, db, schema, colWidths, currentVisibleCols)
			return
		}
		for _, v := range savedViews {
			if v.ID == id {
				currentViewName = v.Name
				currentVisibleCols = v.Columns
				populateTableGrid(win, rowsContainer, db, schema, colWidths, currentVisibleCols)
				return
			}
		}
		// fallback
		currentViewID = 0
		currentViewName = "All"
		currentVisibleCols = nil
		populateTableGrid(win, rowsContainer, db, schema, colWidths, currentVisibleCols)
	}

	// helper to repopulate
	populate := func() {
		loadViews()
		setViewByID(currentViewID)
	}

	// initial populate
	populate()

	// --- Toolbar and view selector --- //

	// build options for select
	buildViewOptions := func() []string {
		opts := []string{"All"}
		for _, v := range savedViews {
			opts = append(opts, v.Name)
		}
		return opts
	}

	// map view name -> id for lookup
	viewNameToID := func(name string) int {
		if name == "All" {
			return 0
		}
		for _, v := range savedViews {
			if v.Name == name {
				return v.ID
			}
		}
		return 0
	}

	// select widget to pick a view
	viewSelect := widget.NewSelect(buildViewOptions(), func(sel string) {
		setViewByID(viewNameToID(sel))
	})
	viewSelect.SetSelected("All")

	// edit button (pencil) and delete button (trash) — use unicode icons for reliability
	editViewBtn := widget.NewButton("✎", func() {
		// if editing "All", create a new view instead (pre-filled with all shown)
		var editing *View
		if currentViewID == 0 {
			editing = &View{ID: 0, Name: "New view", Columns: currentVisibleCols}
		} else {
			for _, v := range savedViews {
				if v.ID == currentViewID {
					copyCols := append([]string(nil), v.Columns...)
					editing = &View{ID: v.ID, Name: v.Name, Columns: copyCols}
					break
				}
			}
		}

		// build dialog content: name entry + checkboxes for all columns
		nameEntry := widget.NewEntry()
		nameEntry.SetText(editing.Name)

		// checkbox map
		checks := map[string]*widget.Check{}
		colsBox := container.NewVBox()
		visibleSet := map[string]bool{}
		for _, c := range editing.Columns {
			visibleSet[c] = true
		}
		for _, f := range schema {
			checked := false
			if len(editing.Columns) == 0 {
				// if editing new "All" view: default checked
				checked = true
			} else {
				if visibleSet[f.Name] {
					checked = true
				}
			}
			ch := widget.NewCheck(f.Label, func(bool) {})
			ch.SetChecked(checked)
			checks[f.Name] = ch
			colsBox.Add(ch)
		}

		form := container.NewVBox(
			widget.NewLabel("View name:"),
			nameEntry,
			widget.NewLabel("Visible columns:"),
			container.NewScroll(colsBox),
		)

		dialog.ShowCustomConfirm("Edit View", "Save", "Cancel", form, func(yes bool) {
			if !yes {
				return
			}
			// collect selected columns
			var selCols []string
			for _, f := range schema {
				if ch, ok := checks[f.Name]; ok && ch.Checked {
					selCols = append(selCols, f.Name)
				}
			}
			// if editing existing view
			if editing.ID > 0 {
				if err := updateView(db, editing.ID, nameEntry.Text, selCols); err != nil {
					dialog.ShowError(err, win)
					return
				}
			} else {
				if _, err := insertView(db, nameEntry.Text, selCols); err != nil {
					dialog.ShowError(err, win)
					return
				}
			}
			// reload views and set to the saved/edited view
			loadViews()
			// find the view id by name (prefer exact match)
			for _, vv := range savedViews {
				if vv.Name == nameEntry.Text {
					setViewByID(vv.ID)
					viewSelect.Options = buildViewOptions()
					viewSelect.SetSelected(vv.Name)
					return
				}
			}
			// fallback
			viewSelect.Options = buildViewOptions()
			viewSelect.SetSelected("All")
			setViewByID(0)
		}, win)
	})

	delViewBtn := widget.NewButton("🗑", func() {
		// cannot delete "All"
		dialog.ShowConfirm("Delete view", "Delete this view? This cannot be undone.", func(yes bool) {
			if !yes {
				return
			}
			if err := deleteView(db, currentViewID); err != nil {
				dialog.ShowError(err, win)
				return
			}
			loadViews()
			viewSelect.Options = buildViewOptions()
			viewSelect.SetSelected("All")
			setViewByID(0)
		}, win)
	})

	// keep delete enabled/disabled in populate
	updateViewButtons := func() {
		if currentViewID != 0 {
			delViewBtn.Disable()
		} else {
			delViewBtn.Enable()
		}
		viewSelect.Options = buildViewOptions()
		viewSelect.Refresh()
	}

	// --- toolbar buttons (Open/Save/Print/New/Add) --- //
	openBtn := widget.NewButton("Open file", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if r == nil {
				return
			}
			defer r.Close()
			data, err := io.ReadAll(r)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}

			// detect if file is an array of entries or an object { "entries": [...], "views":[...] }
			var j interface{}
			if err := json.Unmarshal(data, &j); err != nil {
				dialog.ShowError(err, win)
				return
			}

			// clear existing entries
			if _, err := db.Exec("DELETE FROM entries"); err != nil {
				dialog.ShowError(err, win)
				return
			}

			switch t := j.(type) {
			case []interface{}:
				// legacy array of objects
				var entries []map[string]interface{}
				if err := json.Unmarshal(data, &entries); err != nil {
					dialog.ShowError(err, win)
					return
				}
				for _, e := range entries {
					merged := mergeWithSchema(schema, e)
					if _, err := insertRow(db, merged); err != nil {
						dialog.ShowError(err, win)
						return
					}
				}
			case map[string]interface{}:
				// expecting keys "entries" and optional "views"
				// entries
				if rawEntries, ok := t["entries"]; ok {
					entriesBytes, _ := json.Marshal(rawEntries)
					var entries []map[string]interface{}
					if err := json.Unmarshal(entriesBytes, &entries); err != nil {
						dialog.ShowError(err, win)
						return
					}
					for _, e := range entries {
						merged := mergeWithSchema(schema, e)
						if _, err := insertRow(db, merged); err != nil {
							dialog.ShowError(err, win)
							return
						}
					}
				}
				// views (optional)
				if rawViews, ok := t["views"]; ok {
					viewsBytes, _ := json.Marshal(rawViews)
					var views []struct {
						Name    string   `json:"Name"`
						Columns []string `json:"Columns"`
					}
					// tolerate several shapes: try to unmarshal into expected struct
					_ = json.Unmarshal(viewsBytes, &views)

					// delete all existing views then insert
					if err := deleteAllViews(db); err != nil {
						dialog.ShowError(err, win)
						return
					}
					for _, v := range views {
						if _, err := insertView(db, v.Name, v.Columns); err != nil {
							dialog.ShowError(err, win)
							return
						}
					}
				}
			default:
				dialog.ShowError(fmt.Errorf("unknown import format"), win)
				return
			}

			// reload views and data
			populate()
			dialog.ShowInformation("Import", "Imported data", win)
		}, win)
		fd.SetFilter(storageFilterJSON())
		fd.Show()
	})

	saveBtn := widget.NewButton("Save file", func() {
		fd := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if uc == nil {
				return
			}
			defer uc.Close()
			rows, err := getAllRows(db)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			var entries []map[string]interface{}
			for _, r := range rows {
				objData := mergeWithSchema(schema, r.Data)
				obj := attachIDToDataMap(r.ID, objData)
				entries = append(entries, obj)
			}

			// include views
			views, err := getAllViews(db)
			if err != nil {
				// non-fatal: continue with empty views
				views = nil
			}
			var exportedViews []map[string]interface{}
			for _, v := range views {
				exportedViews = append(exportedViews, map[string]interface{}{
					"Name":    v.Name,
					"Columns": v.Columns,
				})
			}

			out := map[string]interface{}{
				"entries": entries,
				"views":   exportedViews,
			}

			data, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if _, err := uc.Write(data); err != nil {
				dialog.ShowError(err, win)
				return
			}
		}, win)
		fd.SetFileName("export.json")
		fd.Show()
	})

	printBtn := widget.NewButton("Print", func() {
		rows, err := getAllRows(db)
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("ID: %d\n", r.ID))
			for _, f := range schema {
				val := mergeWithSchema(schema, r.Data)[f.Name]
				b.WriteString(fmt.Sprintf("%s: %v\n", f.Name, val))
			}
			b.WriteString("\n")
		}
		tmpName, err := os.CreateTemp("", "spreadsheet_print_*.txt")
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		tmpName.WriteString(b.String())
		tmpName.Close()
		if err := exec.Command("lpr", tmpName.Name()).Run(); err != nil {
			dialog.ShowError(fmt.Errorf("print failed: %w", err), win)
			return
		}
		dialog.ShowInformation("Print", "Sent to printer", win)
	})

	addRowBtn := widget.NewButton("Add Row", func() {
		empty := getEmptyRowFromSchema(schema)
		if _, err := insertRow(db, empty); err != nil {
			dialog.ShowError(err, win)
			return
		}
		populate()
	})

	// New DB button: confirm and create a fresh DB file (will remove existing data.db)
	newDBBtn := widget.NewButton("New DB", func() {
		dialog.ShowConfirm("New Database", "This will erase current data and create a new blank database. Continue?", func(yes bool) {
			if !yes {
				return
			}
			if db != nil {
				if err := db.Close(); err != nil {
					log.Printf("warning: failed to close DB: %v", err)
				}
			}
			if err := os.Remove("./data.db"); err != nil && !os.IsNotExist(err) {
				dialog.ShowError(fmt.Errorf("failed to remove existing DB: %w", err), win)
				return
			}
			db = initializeDB()
			// create one blank row using schema defaults
			if _, err := insertRow(db, getEmptyRowFromSchema(schema)); err != nil {
				dialog.ShowError(err, win)
				return
			}
			// clear views
			_ = deleteAllViews(db)
			populate()
			dialog.ShowInformation("New Database", "Created new blank database", win)
		}, win)
	})

	// toolbar: view selector + edit/delete + separators + other buttons
	viewToolbar := container.NewHBox(viewSelect, editViewBtn, delViewBtn)
	toolbar := container.NewHBox(newDBBtn, widget.NewSeparator(), viewToolbar, widget.NewSeparator(), openBtn, saveBtn, printBtn, widget.NewSeparator(), addRowBtn)

	// scrollable area should allow both axes
	scroll := container.NewScroll(rowsContainer)
	scroll.SetMinSize(fyne.NewSize(600, 300))

	// ensure buttons reflect current view state
	updateViewButtons()

	// Return the UI
	return container.NewBorder(toolbar, nil, nil, nil, scroll)
}

// storageFilterJSON returns a file dialog filter for .json
func storageFilterJSON() storage.FileFilter {
	return storage.NewExtensionFileFilter([]string{".json"})
}

// populateTableGrid rebuilds header + rows in a VBox so header and cells use same widths.
// visibleCols = nil => show all columns; otherwise restrict to those names (in order of schema)
func populateTableGrid(win fyne.Window, rowsContainer *fyne.Container, db *sql.DB, schema []FieldDef, colWidths []float32, visibleCols []string) {
	rows, err := getAllRows(db)
	if err != nil {
		log.Println("Error loading data:", err)
		rowsContainer.Objects = []fyne.CanvasObject{widget.NewLabel("Error loading data")}
		rowsContainer.Refresh()
		return
	}

	rowsContainer.Objects = nil

	// build a set for visible columns if provided
	showAll := len(visibleCols) == 0
	visSet := map[string]bool{}
	for _, n := range visibleCols {
		visSet[n] = true
	}

	// build effective schema in order
	effective := []FieldDef{}
	for _, f := range schema {
		if showAll || visSet[f.Name] {
			effective = append(effective, f)
		}
	}

	// column resizing constraints
	const minColWidth = 40.0
	// default heights
	const singleLineHeight = 30.0
	const listItemHeight = 20.0
	const listMaxVisible = 3

	// Header row
	headerBg := canvas.NewRectangle(color.NRGBA{R: 240, G: 240, B: 240, A: 20})
	headerRow := container.NewHBox()
	for ci, f := range effective {
		label := widget.NewLabel(f.Label)
		label.Alignment = fyne.TextAlignLeading

		cell := container.NewStack(headerBg, container.NewHBox(label))

		// adjust index into colWidths (keep using original column indices if available)
		widx := ci
		if widx >= len(colWidths) {
			widx = len(colWidths) - 1
		}

		// resizer clamps to minimum width
		res := newColResizer(func(dx float32) {
			newW := float32(math.Max(minColWidth, float64(colWidths[widx]+dx)))
			if newW != colWidths[widx] {
				colWidths[widx] = newW
				populateTableGrid(win, rowsContainer, db, schema, colWidths, visibleCols)
			}
		})

		cellWrap := container.New(layout.NewGridWrapLayout(fyne.NewSize(colWidths[widx], singleLineHeight)), cell)
		headerRow.Add(container.NewHBox(cellWrap, res))
	}
	// Actions header (no resizer)
	actionLabel := widget.NewLabel("Actions")
	actionCell := container.NewStack(headerBg, actionLabel)
	actWrap := container.New(layout.NewGridWrapLayout(fyne.NewSize(colWidths[len(colWidths)-1], singleLineHeight)), actionCell)
	headerRow.Add(actWrap)

	rowsContainer.Add(headerRow)

	// Get executable directory for link files
	exeDir := ""
	if p, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(p)
	}

	// Rows
	for ri, r := range rows {
		mergedData := mergeWithSchema(schema, r.Data)

		// compute row height dynamically: base singleLineHeight but expand for []string fields
		rowH := singleLineHeight
		for _, f := range effective {
			if f.Type == "[]string" {
				var listLen int
				if v, ok := mergedData[f.Name]; ok {
					switch t := v.(type) {
					case []interface{}:
						listLen = len(t)
					case []string:
						listLen = len(t)
					default:
						if s := fmt.Sprintf("%v", v); s != "" {
							listLen = 1
						}
					}
				}
				visible := listLen
				if visible > listMaxVisible {
					visible = listMaxVisible
				}
				h := float32(visible)*listItemHeight + 6 // padding
				if h > float32(rowH) {
					rowH = float64(h)
				}
			}
		}

		// alternating row color
		var bg color.NRGBA
		if ri%2 == 0 {
			bg = color.NRGBA{R: 250, G: 250, B: 250, A: 255}
		} else {
			bg = color.NRGBA{R: 245, G: 245, B: 255, A: 200}
		}

		rowBox := container.NewHBox()
		for ci, f := range effective {
			rect := canvas.NewRectangle(bg)
			var cell fyne.CanvasObject

			// column width index
			widx := ci
			if widx >= len(colWidths) {
				widx = len(colWidths) - 1
			}

			switch f.Type {
			case "int":
				if strings.EqualFold(f.Name, "ID") {
					lbl := widget.NewLabel(fmt.Sprintf("%v", r.ID))
					// center vertically
					cell = container.NewVBox(layout.NewSpacer(), container.NewHBox(lbl), layout.NewSpacer())
				} else {
					val := ""
					if v, ok := mergedData[f.Name]; ok {
						switch t := v.(type) {
						case float64:
							val = strconv.Itoa(int(t))
						case int:
							val = strconv.Itoa(t)
						case string:
							val = t
						}
					}
					entry := widget.NewEntry()
					entry.SetText(val)
					localID := r.ID
					fieldName := f.Name
					entry.OnChanged = func(s string) {
						if s == "" {
							s = "0"
						}
						v, err := strconv.Atoi(s)
						if err != nil {
							return
						}
						_ = updateField(db, localID, fieldName, v)
					}
					// vertically center single line
					cell = container.NewVBox(layout.NewSpacer(), entry, layout.NewSpacer())
				}
			case "string":
				val := ""
				if v, ok := mergedData[f.Name]; ok {
					if s, ok := v.(string); ok {
						val = s
					} else {
						val = fmt.Sprintf("%v", v)
					}
				}
				entry := widget.NewEntry()
				entry.SetText(val)
				localID := r.ID
				fieldName := f.Name
				entry.OnChanged = func(s string) {
					_ = updateField(db, localID, fieldName, s)
				}
				cell = container.NewVBox(layout.NewSpacer(), entry, layout.NewSpacer())
			case "link":
				// show label (blue/red) and support left-click => edit, right-click => open file
				curText := ""
				if v, ok := mergedData[f.Name]; ok {
					if s, ok := v.(string); ok {
						curText = s
					} else {
						curText = fmt.Sprintf("%v", v)
					}
				}
				// determine color by existence
				txtColor := color.NRGBA{R: 0, G: 0, B: 200, A: 255} // blue
				if curText != "" {
					full := filepath.Join(exeDir, curText)
					if _, err := os.Stat(full); err != nil {
						// not found => red
						txtColor = color.NRGBA{R: 200, G: 0, B: 0, A: 255}
					}
					fmt.Printf("Link field value: %s (full path: %s)\n", curText, full)
				}
				label := canvas.NewText(curText, txtColor)
				label.TextStyle = fyne.TextStyle{Italic: false}

				// container to swap between label and entry
				swap := container.NewStack(label)

				// declare overlay first so closures can reference it safely
				var overlay *clickableOverlay

				// overlay handlers
				onLeft := func() {
					// replace with an entry for editing
					entry := widget.NewEntry()
					entry.SetText(label.Text)
					localID := r.ID
					fieldName := f.Name

					entry.OnSubmitted = func(s string) {
						_ = updateField(db, localID, fieldName, s)
						// update label text & color
						cur := s
						colorNow := color.NRGBA{R: 0, G: 0, B: 200, A: 255}
						if cur != "" {
							full := filepath.Join(exeDir, cur)
							if _, err := os.Stat(full); err != nil {
								colorNow = color.NRGBA{R: 200, G: 0, B: 0, A: 255}
							}
						}
						label.Text = cur
						label.Color = colorNow
						label.Refresh()
						// swap back to label and show overlay again
						swap.Objects = []fyne.CanvasObject{label}
						swap.Refresh()
						if overlay != nil {
							overlay.Show()
						}
					}
					// persist live changes while editing
					entry.OnChanged = func(s string) {
						_ = updateField(db, localID, fieldName, s)
					}

					// hide overlay so the entry can receive focus and keyboard input
					if overlay != nil {
						overlay.Hide()
					}
					swap.Objects = []fyne.CanvasObject{entry}
					swap.Refresh()
					// focus the entry so the user can type immediately
					win.Canvas().Focus(entry)
				}

				onRight := func() {
					// try to open file; if fails, mark red
					target := filepath.Join(exeDir, label.Text)
					if label.Text == "" {
						dialog.ShowInformation("Open link", "No file specified", win)
						return
					}
					if err := exec.Command("xdg-open", target).Start(); err != nil {
						// mark red
						label.Color = color.NRGBA{R: 200, G: 0, B: 0, A: 255}
						label.Refresh()
						dialog.ShowError(fmt.Errorf("open failed: %w", err), win)
					}
				}

				// now create the overlay with the handlers
				overlay = newClickableOverlay(onLeft, onRight)
				cell = container.NewStack(swap, overlay)
			case "[]string":
				var list []string
				if v, ok := mergedData[f.Name]; ok {
					switch t := v.(type) {
					case []interface{}:
						for _, it := range t {
							list = append(list, fmt.Sprintf("%v", it))
						}
					case []string:
						list = append(list, t...)
					default:
						if tstr := fmt.Sprintf("%v", v); tstr != "" {
							list = append(list, tstr)
						}
					}
				}
				// show up to listMaxVisible items, scrolling for more
				editor := makeListEditorInline(win, db, r.ID, list, f.Name)
				// try to set visible height
				if sc, ok := editor.(*container.Scroll); ok {
					visible := len(list)
					if visible > listMaxVisible {
						visible = listMaxVisible
					}
					if visible == 0 {
						visible = 1
					}
					h := float32(visible)*listItemHeight + 6
					sc.SetMinSize(fyne.NewSize(colWidths[widx], h))
				}
				cell = container.NewVBox(editor)
			default:
				val := ""
				if v, ok := mergedData[f.Name]; ok {
					val = fmt.Sprintf("%v", v)
				}
				entry := widget.NewEntry()
				entry.SetText(val)
				localID := r.ID
				fieldName := f.Name
				entry.OnChanged = func(s string) {
					_ = updateField(db, localID, fieldName, s)
				}
				cell = container.NewVBox(layout.NewSpacer(), entry, layout.NewSpacer())
			}

			cellWrap := container.New(layout.NewGridWrapLayout(fyne.NewSize(colWidths[widx], float32(rowH))), container.NewStack(rect, cell))
			rowBox.Add(cellWrap)
		}

		// Actions (trash icon button)
		trash := widget.NewButton("🗑", func() {
			dialog.ShowConfirm("Delete", "Delete this row?", func(yes bool) {
				if !yes {
					return
				}
				_ = deleteRow(db, r.ID)
				populateTableGrid(win, rowsContainer, db, schema, colWidths, visibleCols)
			}, win)
		})
		actWrap := container.New(layout.NewGridWrapLayout(fyne.NewSize(colWidths[len(colWidths)-1], float32(rowH))), container.NewStack(canvas.NewRectangle(bg), trash))
		rowBox.Add(actWrap)

		rowsContainer.Add(rowBox)
	}

	rowsContainer.Refresh()
}

// makeListEditorInline is an inline vertical editor for []string that persists on change.
// behavior:
// - trailing blank entry always present for quick add
// - pressing Enter on last entry appends new blank
// - empty non-last entries are removed
func makeListEditorInline(win fyne.Window, db *sql.DB, entryID int, initial []string, fieldName string) fyne.CanvasObject {
	listContainer := container.NewVBox()
	entries := make([]*widget.Entry, 0, len(initial)+1)

	save := func() {
		var out []string
		for _, e := range entries {
			if s := strings.TrimSpace(e.Text); s != "" {
				out = append(out, s)
			}
		}
		_ = updateField(db, entryID, fieldName, out)
	}

	var createEntry func(string) *widget.Entry
	createEntry = func(text string) *widget.Entry {
		e := widget.NewEntry()
		e.SetText(text)

		e.OnSubmitted = func(sub string) {
			sub = strings.TrimSpace(sub)
			if sub == "" {
				return
			}
			// if this is last entry append another
			if len(entries) > 0 && entries[len(entries)-1] == e {
				entries = append(entries, createEntry(""))
				listContainer.Add(entries[len(entries)-1])
			}
			save()
		}

		e.OnChanged = func(_ string) {
			trim := strings.TrimSpace(e.Text)
			// find idx
			idx := -1
			for i, en := range entries {
				if en == e {
					idx = i
					break
				}
			}
			if idx == -1 {
				return
			}
			// remove empty non-last
			if trim == "" && idx != len(entries)-1 {
				entries = append(entries[:idx], entries[idx+1:]...)
				listContainer.Objects = nil
				for _, en := range entries {
					listContainer.Add(en)
				}
				// ensure trailing blank
				if len(entries) == 0 || strings.TrimSpace(entries[len(entries)-1].Text) != "" {
					entries = append(entries, createEntry(""))
					listContainer.Add(entries[len(entries)-1])
				}
				save()
				return
			}
			// otherwise save current state
			save()
		}
		return e
	}

	for _, v := range initial {
		entries = append(entries, createEntry(v))
		listContainer.Add(entries[len(entries)-1])
	}
	entries = append(entries, createEntry(""))
	listContainer.Add(entries[len(entries)-1])

	scroll := container.NewVScroll(listContainer)
	scroll.SetMinSize(fyne.NewSize(140, 30))
	return scroll
}

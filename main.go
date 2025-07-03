package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
)

// --- Journal Data Structure ---
type MarkdownEntry struct {
	CreationDate time.Time
	Title        string
	MarkdownText string
	// Media map: original source path -> new file name in media subdir
	Media map[string]string
}

// --- Global Markdown Converter ---
var markdownConverter *md.Converter

func init() {
	markdownConverter = md.NewConverter("", true, nil)
}

// --- Helper Functions ---





func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}


	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip vulnerability
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("%s: illegal file path", fpath)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close() // Close outFile before returning error
			return err
		}

		_, err = io.Copy(outFile, rc)

		// Close files inside the loop
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}

// parseAppleDate parses dates like "Wednesday, May 14, 2025" or "Tuesday, December 12, 2023"
func parseAppleDate(dateStr string) (time.Time, error) {
	// Normalize by removing the day of the week part
	parts := strings.SplitN(dateStr, ",", 2)
	if len(parts) == 2 {
		dateStr = strings.TrimSpace(parts[1]) // "May 14, 2025" or "December 12, 2023"
	}

	// Try parsing "January 2, 2006" format
	layouts := []string{
		"January 2, 2006", // For "May 14, 2025"
		"Jan 2, 2006",     // Just in case
	}
	var t time.Time
	var err error
	for _, layout := range layouts {
		t, err = time.Parse(layout, dateStr)
		if err == nil {
			// Set time to noon UTC for consistency, as Apple Journal HTML doesn't provide time
			t = time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, time.UTC)
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("failed to parse date string '%s' with known layouts: %w", dateStr, err)
}


func processEntryHTML(htmlFilePath string, baseResourcesPath string) (MarkdownEntry, error) {
	file, err := os.Open(htmlFilePath)
	if err != nil {
		return MarkdownEntry{}, fmt.Errorf("opening HTML file %s: %w", htmlFilePath, err)
	}
	defer file.Close()

	doc, err := goquery.NewDocumentFromReader(file)
	if err != nil {
		return MarkdownEntry{}, fmt.Errorf("parsing HTML file %s: %w", htmlFilePath, err)
	}

	entry := MarkdownEntry{
		Media: make(map[string]string),
	}

	// --- Extract Date ---
	dateStr := strings.TrimSpace(doc.Find("div.pageHeader").First().Text())
	if dateStr == "" {
		return MarkdownEntry{}, fmt.Errorf("no date found in pageHeader for %s", htmlFilePath)
	}
	creationTime, err := parseAppleDate(dateStr)
	if err != nil {
		return MarkdownEntry{}, fmt.Errorf("could not parse date '%s' for %s: %w", dateStr, htmlFilePath, err)
	}
	entry.CreationDate = creationTime

	// --- Extract Title ---
	titleSelection := doc.Find("div.title span.s2").First()
	if titleSelection.Length() > 0 {
		entry.Title = strings.TrimSpace(titleSelection.Text())
	} else {
		// Fallback for titles in different structures or use filename
		fn := filepath.Base(htmlFilePath)
		fn = strings.TrimSuffix(fn, filepath.Ext(fn))
		parts := strings.SplitN(fn, "_", 2)
		if len(parts) > 1 && strings.Contains(parts[0], "-") {
			entry.Title = strings.ReplaceAll(parts[1], "_", " ")
		}
	}

	// --- Extract Body Content & Media ---
	var bodyMarkdownBuilder strings.Builder
	doc.Find("div.pageContainer").Children().Each(func(i int, s *goquery.Selection) {
		if s.Is("div.pageHeader, div.title") {
			return // Skip header and title as they are already processed
		}

		if s.Is("div.assetGrid") {
			s.Find("div.gridItem.assetType_photo img.asset_image").Each(func(j int, imgSel *goquery.Selection) {
				imgSrc, exists := imgSel.Attr("src")
				if !exists {
					return
				}

				originalImageName := filepath.Base(imgSrc)
				newImageName := fmt.Sprintf("%s-%s", uuid.New().String(), originalImageName)
				absImgSrc := filepath.Clean(filepath.Join(filepath.Dir(htmlFilePath), imgSrc))

				if _, err := os.Stat(absImgSrc); os.IsNotExist(err) {
					log.Printf("Warning: Image file not found: %s (referenced in %s)", absImgSrc, htmlFilePath)
					return
				}

				entry.Media[absImgSrc] = newImageName
				bodyMarkdownBuilder.WriteString(fmt.Sprintf("![](media/%s)\n\n", newImageName))
			})
		} else {
			htmlContent, err := goquery.OuterHtml(s)
			if err != nil {
				log.Printf("Warning: Could not get HTML content for a section in %s: %v", htmlFilePath, err)
				return
			}

			markdownFrag, err := markdownConverter.ConvertString(htmlContent)
			if err != nil {
				log.Printf("Warning: Markdown conversion error for a fragment in %s: %v", htmlFilePath, err)
			} else {
				bodyMarkdownBuilder.WriteString(strings.TrimSpace(markdownFrag) + "\n\n")
			}
		}
	})

	finalMarkdown := strings.TrimSpace(bodyMarkdownBuilder.String())
	if entry.Title != "" {
		entry.MarkdownText = fmt.Sprintf("# %s\n\n%s", entry.Title, finalMarkdown)
	} else {
		entry.MarkdownText = finalMarkdown
	}

	if entry.MarkdownText == "" && len(entry.Media) == 0 {
		return MarkdownEntry{}, fmt.Errorf("empty entry after processing %s", htmlFilePath)
	}

	return entry, nil
}


func saveMarkdownFile(outputDir string, entry MarkdownEntry) error {
	// Create media subdirectory if it doesn't exist
	mediaDir := filepath.Join(outputDir, "media")
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		return fmt.Errorf("creating media directory %s: %w", mediaDir, err)
	}

	// Copy media files
	for src, newName := range entry.Media {
		dst := filepath.Join(mediaDir, newName)
		if err := copyFile(src, dst); err != nil {
			log.Printf("Warning: Failed to copy media file from %s to %s: %v", src, dst, err)
			// Continue trying to save the rest of the entry
		}
	}

	// Sanitize title for file name
	safeTitle := strings.ReplaceAll(entry.Title, "/", "-")
	safeTitle = strings.ReplaceAll(safeTitle, "\"", "'")
	// Further sanitization can be added here

	// Create markdown file
	datePrefix := entry.CreationDate.Format("2006-01-02")
	fileName := fmt.Sprintf("%s-%s.md", datePrefix, safeTitle)
	filePath := filepath.Join(outputDir, fileName)

	err := os.WriteFile(filePath, []byte(entry.MarkdownText), 0644)
	if err != nil {
		return fmt.Errorf("writing markdown file %s: %w", filePath, err)
	}

	log.Printf("Successfully saved entry to %s", filePath)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}


func main() {
	inputZip := flag.String("i", "", "Input Apple Journal ZIP file path (required)")
	outputDir := flag.String("o", "", "Output directory for Markdown files (required)")
	flag.Parse()

	if *inputZip == "" || *outputDir == "" {
		fmt.Println("Both input (-i) and output (-o) are required.")
		flag.Usage()
		os.Exit(1)
	}

	log.Printf("Starting conversion from %s to %s", *inputZip, *outputDir)

	// 1. Create output directory if it doesn't exist
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// 2. Create temp directory for extraction
	tempExtractDir, err := os.MkdirTemp("", "applejournal_extract_*")
	if err != nil {
		log.Fatalf("Failed to create temp directory: %v", err)
	}
	defer func() {
		log.Printf("Cleaning up temp directory: %s", tempExtractDir)
		if err := os.RemoveAll(tempExtractDir); err != nil {
			log.Printf("Warning: Failed to remove temp directory %s: %v", tempExtractDir, err)
		}
	}()

	// 3. Unzip input Apple Journal zip
	if err := unzip(*inputZip, tempExtractDir); err != nil {
		log.Fatalf("Failed to unzip %s: %v", *inputZip, err)
	}

	// 4. Find Entries path
	entriesPath := filepath.Join(tempExtractDir, "Entries")
    if _, err := os.Stat(entriesPath); os.IsNotExist(err) {
        filesInTemp, _ := os.ReadDir(tempExtractDir)
        if len(filesInTemp) == 1 && filesInTemp[0].IsDir() {
            potentialRoot := filepath.Join(tempExtractDir, filesInTemp[0].Name(), "Entries")
            if _, err := os.Stat(potentialRoot); err == nil {
                entriesPath = potentialRoot
            }
        }
    }

	if _, err := os.Stat(entriesPath); os.IsNotExist(err) {
		log.Fatalf("Entries folder not found at %s. Please ensure the zip structure is correct.", entriesPath)
	}

	// 5. Process entries
	log.Printf("Processing HTML entries from: %s", entriesPath)
	processedCount := 0
	err = filepath.WalkDir(entriesPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Printf("Error accessing path %s: %v. Skipping.", path, walkErr)
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".html") {
			return nil
		}

		log.Printf("Processing entry: %s", path)
		entry, procErr := processEntryHTML(path, "") // baseResourcesPath is not used anymore
		if procErr != nil {
			log.Printf("Error processing entry %s: %v. Entry skipped.", path, procErr)
			return nil
		}

		if err := saveMarkdownFile(*outputDir, entry); err != nil {
			log.Printf("Error saving markdown file for %s: %v", path, err)
		}
		processedCount++
		return nil
	})

	if err != nil {
		log.Fatalf("Error walking through entries directory %s: %v", entriesPath, err)
	}

	log.Printf("Conversion complete! Processed %d entries.", processedCount)
	log.Printf("Output written to: %s", *outputDir)
}


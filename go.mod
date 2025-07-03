module journalconverter

go 1.21 // Or your Go version, 1.18+ for generics if we were to use them heavily

require (
	github.com/JohannesKaufmann/html-to-markdown v1.6.0
	github.com/PuerkitoBio/goquery v1.9.2 // Switched to goquery for easier DOM traversal
	github.com/google/uuid v1.6.0
)

require (
	github.com/andybalholm/cascadia v1.3.2 // indirect
	golang.org/x/net v0.25.0 // indirect
)

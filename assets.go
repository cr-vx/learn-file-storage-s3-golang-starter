package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)

func (cfg apiConfig) ensureAssetsDir() error {
	if _, err := os.Stat(cfg.assetsRoot); os.IsNotExist(err) {
		return os.Mkdir(cfg.assetsRoot, 0755)
	}
	return nil
}

func generateRandomNameWithExtensionType(mediaType string) string {
	byteSlice := make([]byte, 32)
	_, err := rand.Read(byteSlice)
	if err != nil {
		panic("failed to generate random bytes")
	}
	id := base64.RawURLEncoding.EncodeToString(byteSlice)

	ext := mediaTypeToExtension(mediaType)
	return fmt.Sprintf("%s%s", id, ext)
}

func (cfg apiConfig) getAssetDiskPath(assetPath string) string {
	return filepath.Join(cfg.assetsRoot, assetPath)
}

// getAssetURL creates and returns formatted URL using assetPath
func (cfg apiConfig) getAssetURL(assetPath string) string {
	return fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, assetPath)
}

func (cfg apiConfig) getObjectURL(key string) string {
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)
}

func mediaTypeToExtension(mediaType string) string {
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 {
		return ".bin"
	}
	return "." + parts[1]
}

// getVideoAspectRatio uses ffprobe to retrieve the video's width and height.
// It calculates the aspect ratio, returning:
//   - "16:9" for 16:9 ratio
//   - "9:16" for 9:16 ratio
//   - "other" for any other ratio
//
// If there's an error it returns an empty string and an error.
func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command(
		"ffprobe", "-v",
		"error", "-print_format",
		"json", "-show_streams",
		filePath,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe error: %s\nCommand failed with: %v", stderr.String(), err)
	}

	var output struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return "", fmt.Errorf("couldn't parse ffprobe output: %v", err)
	}

	if len(output.Streams) == 0 {
		return "", errors.New("no video streams found")
	}

	width := output.Streams[0].Width
	height := output.Streams[0].Height
	ratio := calculateAspectRatio(width, height)

	return ratio, nil
}

func calculateAspectRatio(width, height int) string {
	if width == 16*height/9 { // 16:9
		return "landscape"
	} else if height == 16*width/9 { // 9:16
		return "portrait"
	}
	return "other"
}

// processVideoForFastStart uses ffmpeg to create an MP4 with fast start.
// It returns the filepath of the encoded video or an error if processing fails.
func processVideoForFastStart(inputFilePath string) (string, error) {
	log.Println("Beginning fast start encoding...")
	faststartPath := fmt.Sprintf("%s.processing", inputFilePath)
	cmd := exec.Command(
		"ffmpeg",
		"-i", inputFilePath, // "-i": Input file option, followed by the path of the input file.
		"-c", "copy", // "-c copy": Copy the codecs from the input to the output without re-encoding.
		"-movflags", "faststart", // "-movflags faststart": Enables faststart for the MP4.
		"-f", "mp4", // "-f mp4": Force the output format to be MP4.
		faststartPath, // faststartPath: The destination path for the processed video file.
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg error: %s\nCommand failed with: %v", stderr.String(), err)
	}
	fileInfo, err := os.Stat(faststartPath)
	if err != nil {
		return "", fmt.Errorf("couldn't stat processed file: %v", err)
	}
	if fileInfo.Size() == 0 {
		return "", errors.New("processed file is empty")
	}

	log.Printf("Encoding for '%s' complete: %d bytes\n", fileInfo.Name(), fileInfo.Size())
	return faststartPath, nil

}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	signedClient := s3.NewPresignClient(s3Client)
	presignReq, err := signedClient.PresignGetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", fmt.Errorf("couldn't create presign URL %v", err)
	}

	return presignReq.URL, nil
}

// dbVideoToSignedVideo returns the video with presigned URL
func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	urlParts := strings.Split(*video.VideoURL, ",")
	if len(urlParts) < 2 {
		return video, nil
	}
	bucket := urlParts[0]
	key := urlParts[1]
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 10*time.Minute)
	if err != nil {
		return video, err
	}
	video.VideoURL = &presignedURL

	return video, nil
}

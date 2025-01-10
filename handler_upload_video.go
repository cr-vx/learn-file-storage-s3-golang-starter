package main

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

const maxUploadLimit = 1 << 30 // 1GB

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadLimit)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}
	// validate video ownership
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not authorized to update video", err)
	}

	// handle video file
	file, fileHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(fileHeader.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid media type, only mp4 is supported", nil)
		return
	}

	tempVidFile, err := os.CreateTemp("", "tubely-upload_*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Issue creating temp file", err)
		return
	}
	defer os.Remove(tempVidFile.Name())
	defer tempVidFile.Close()

	if _, err := io.Copy(tempVidFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't write file to disk", err)
		return
	}
	_, err = tempVidFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't reset file pointer", err)
		return
	}

	// process vid for fast start
	processedFilePath, err := processVideoForFastStart(tempVidFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't process video", err)
		return
	}
	defer os.Remove(processedFilePath)

	fastEncodedVid, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't open encoded file", err)
	}
	defer fastEncodedVid.Close()

	// handle video metadata
	aspectRatio, err := getVideoAspectRatio(tempVidFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't handle aspect ratio", err)
		return
	}

	//generate key for s3 with aspect ratio as prefix
	key := generateRandomNameWithExtensionType(mediaType)
	key = filepath.Join(aspectRatio, key)

	// Upload to S3
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(key),
		Body:        fastEncodedVid,
		ContentType: aws.String(mediaType),
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Issue uploading video to S3", err)
		return
	}

	url := cfg.getObjectURL(key)
	video.VideoURL = &url
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video information", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
}

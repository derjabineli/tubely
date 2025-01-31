package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// maxMemory := 1 << 30 // 1 GB
	// videoReader := http.MaxBytesReader(w, r.Body, int64(maxMemory))

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	dbVideo, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}
	if dbVideo.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized User", err)
		return
	}

	uploadedVideo, uploadedVideoHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't access uploaded video data", err)
	}
	defer uploadedVideo.Close()

	mediatype, _, err := mime.ParseMediaType(uploadedVideoHeader.Header.Get("Content-Type"))
	if mediatype != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type. Video must be in mp4 format", err)
		return
	}

	tempVideoFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, err.Error(), err)
	}
	defer os.Remove(tempVideoFile.Name())
	defer tempVideoFile.Close()

	_, err = io.Copy(tempVideoFile, uploadedVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save video file", err)
		return
	}

	tempVideoFile.Seek(0, io.SeekStart)

	b := make([]byte, 32)
	_, err = rand.Read(b)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, err.Error(), err)
		return
	}
	key := base64.RawURLEncoding.EncodeToString(b)
	fileName := fmt.Sprintf("%v.%v", key, "mp4")

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{Bucket: &cfg.s3Bucket, Key: &fileName, Body: tempVideoFile, ContentType: &mediatype})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload video", err)
		return
	}

	newVideoURL := fmt.Sprintf("https://%v.s3.%v.amazonaws.com/%v", cfg.s3Bucket, cfg.s3Region, fileName)
	dbVideo.VideoURL = &newVideoURL

	cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update Video", err)
		return
	}

	respondWithJSON(w, http.StatusAccepted, dbVideo)
}

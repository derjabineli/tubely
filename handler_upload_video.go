package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

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
		respondWithError(w, http.StatusInternalServerError, "Failed to create temporary video", err)
	}
	defer os.Remove(tempVideoFile.Name())
	defer tempVideoFile.Close()

	if _, err = io.Copy(tempVideoFile, uploadedVideo); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save video file", err)
		return
	}

	aspectRatio, err := getVideoAspectRatio(tempVideoFile.Name())
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video aspect ratio", err)
		return
	}

	var ratioPrefix string
	switch aspectRatio{
	case "16:9":
		ratioPrefix = "landscape"
	case "9:16":
		ratioPrefix = "portrait"
	default:
		ratioPrefix = "other"
	}

	processedVideoFilePath, err := processVideoForFastStart(tempVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't process video", err)
		return
	}

	processedVideoFile, err := os.Open(processedVideoFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't access processed video file", err)
		return
	}
	defer processedVideoFile.Close()

	processedVideoFile.Seek(0, io.SeekStart)

	b := make([]byte, 32)
	_, err = rand.Read(b)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error while creating random string", err)
		return
	}
	key := base64.RawURLEncoding.EncodeToString(b)
	fileName := fmt.Sprintf("%v/%v.%v", ratioPrefix, key, "mp4")

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{Bucket: &cfg.s3Bucket, Key: &fileName, Body: processedVideoFile, ContentType: &mediatype})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload video", err)
		return
	}

	
	newVideoURL := fmt.Sprintf("%v/%v", cfg.s3CfDistribution, fileName)
	fmt.Printf("New Video URL: %v\n", newVideoURL)
	dbVideo.VideoURL = &newVideoURL

	if err = cfg.db.UpdateVideo(dbVideo); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update Video", err)
		return
	}

	respondWithJSON(w, http.StatusAccepted, dbVideo)
}

func getVideoAspectRatio(filePath string) (string, error) {
	type VideoStream struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	
	type FFProbeOutput struct {
		Streams []VideoStream `json:"streams"`
	}

	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}

	var output FFProbeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return "", err
	}

	if len(output.Streams) == 0 {
		return "", errors.New("no video streams found")
	}

	width := output.Streams[0].Width
	height := output.Streams[0].Height

	if width == 16*height/9 {
		return "16:9", nil
	} else if height == 16*width/9 {
		return "9:16", nil
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", 
	"-i", filePath, 
	"-c", "copy", 
	"-movflags", "faststart", 
	"-f", "mp4",
	outputPath)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return outputPath, nil
}
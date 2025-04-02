package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	var maxMemory int64 = 10 << 30 // 1 GB
	err := r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't parse form", err)
		return
	}
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

	fmt.Println("uploading video", videoID, "by user", userID)

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not your video", nil)
		return
	}
	file, header, err := r.FormFile("video")
	defer file.Close()
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", nil)
		return
	}
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video aspect ratio", err)
		return
	}
	defer tempFile.Close()
	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save file", err)
		return
	}
	ratio, err := getVideoAspectRatio(tempFile.Name())
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't seek file", err)
		return
	}
	videoName, err := cfg.getRandomName()
	switch ratio {
	case "16:9":
		videoName = "landscape/" + videoName
	case "9:16":
		videoName = "portrait/" + videoName
	default:
		videoName = "other/" + videoName
	}

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random ID", err)
		return
	}
	fastStart, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't process video", err)
		return
	}
	fastStartTempFile, err := os.Open(fastStart)
	defer os.Remove(fastStart)
	defer fastStartTempFile.Close()
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(videoName),
		Body:        fastStartTempFile,
		ContentType: aws.String(mediaType),
	})
	if err != nil {
		log.Printf("S3 upload error: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload video", err)
		return
	}
	log.Println("no error")
	videoUrl := fmt.Sprintf("http://%s.s3.localhost.localstack.cloud:4566/%s?dummy=amazonaws.com", cfg.s3Bucket, videoName)
	video.VideoURL = &videoUrl
	cfg.db.UpdateVideo(video)

}

func getVideoAspectRatio(filePath string) (string, error) {
	type FFProbeOutput struct {
		Streams []struct {
			DisplayAspectRatio string `json:"display_aspect_ratio"`
		} `json:"streams"`
	}
	//ffprobe -v error -select_streams v:0 -show_entries stream=display_aspect_ratio -of default=noprint_wrappers=1:nokey=1 boots-video-horizontal.mp4
	ffprobeCmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	result := bytes.Buffer{}
	ffprobeCmd.Stdout = &result
	err := ffprobeCmd.Run()
	if err != nil {
		return "", err
	}
	var output FFProbeOutput
	err = json.Unmarshal(result.Bytes(), &output)
	if err != nil {
		return "", err
	}
	if len(output.Streams) == 0 {
		return "", fmt.Errorf("no streams found")
	}
	ratio := output.Streams[0].DisplayAspectRatio
	if ratio == "16:9" || ratio == "9:16" {
		return ratio, nil

	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + "-faststart.mp4"
	//ffprobe -v error -select_streams v:0 -show_entries stream=display_aspect_ratio -of default=noprint_wrappers=1:nokey=1 boots-video-horizontal.mp4
	ffprobeCmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	err := ffprobeCmd.Run()
	if err != nil {
		return "", err
	}

	return outputPath, nil
}

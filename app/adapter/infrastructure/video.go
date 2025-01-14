package infrastructure

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/yuorei/video-server/app/domain"
	"github.com/yuorei/video-server/db/sqlc"
)

type WatchCountJsonType struct {
	Count int `json:"count"`
}

type UploaderID struct {
	ID string `json:"id"`
}

func (i *Infrastructure) CheckUploadAPIRateLimit(ctx context.Context, id string) error {
	var uploaderID UploaderID
	hit, err := getFromRedis(ctx, i.redis, "upload"+id, &uploaderID)
	if err != nil {
		return err
	} else if hit {
		return fmt.Errorf("upload api rate limit")
	}

	return nil
}

func (i *Infrastructure) SetUploadAPIRateLimit(ctx context.Context, id string) error {
	// 24時間のアップロード回数を制限
	err := setToRedis(ctx, i.redis, "upload"+id, 24*time.Hour, &UploaderID{
		ID: id,
	})
	if err != nil {
		return err
	}
	return nil
}

func (i *Infrastructure) GetVideosFromDB(ctx context.Context) ([]*domain.Video, error) {
	var videos []*domain.Video
	dbVideos, err := i.db.Database.GetPublicAndNonAdultNonAdVideos(ctx)
	if err != nil {
		return nil, err
	}

	tags, err := i.db.Database.GetAllVideosTags(ctx)
	if err != nil {
		return nil, err
	}

	for _, dbVideo := range dbVideos {
		video := domain.NewVideo(dbVideo.ID, dbVideo.VideoUrl, dbVideo.ThumbnailImageUrl, dbVideo.Title, &dbVideo.Description.String, []string{}, int(dbVideo.WatchCount), dbVideo.IsPrivate, dbVideo.IsAdult, dbVideo.IsExternalCutout, dbVideo.IsAd, dbVideo.UploaderID, dbVideo.CreatedAt, dbVideo.UpdatedAt)

		for _, tag := range tags {
			if tag.VideoID == dbVideo.ID {
				video.Tags = append(video.Tags, tag.TagName)
			}
		}

		videos = append(videos, video)
	}

	return videos, nil
}

func (i *Infrastructure) GetVideosByUserIDFromDB(ctx context.Context, userID string) ([]*domain.Video, error) {
	var videos []*domain.Video
	dbVideos, err := i.db.Database.GetPublicAndNonAdByUploaderID(ctx, userID)
	if err != nil {
		return nil, err
	}

	tags, err := i.db.Database.GetAllVideosTagsByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, dbVideo := range dbVideos {
		video := domain.NewVideo(dbVideo.ID, dbVideo.VideoUrl, dbVideo.ThumbnailImageUrl, dbVideo.Title, &dbVideo.Description.String, []string{}, int(dbVideo.WatchCount), dbVideo.IsPrivate, dbVideo.IsAdult, dbVideo.IsExternalCutout, dbVideo.IsAd, dbVideo.UploaderID, dbVideo.CreatedAt, dbVideo.UpdatedAt)
		for _, tag := range tags {
			if tag.VideoID == dbVideo.ID {
				video.Tags = append(video.Tags, tag.TagName)
			}
			videos = append(videos, video)
		}
	}
	return videos, nil
}

func (i *Infrastructure) GetVideoFromDB(ctx context.Context, id string) (*domain.Video, error) {
	dbVideo, err := i.db.Database.GetVideo(ctx, id)
	if err != nil {
		return nil, err
	}

	tags, err := i.db.Database.GetVideoTags(ctx, id)
	if err != nil {
		return nil, err
	}

	video := domain.NewVideo(dbVideo.ID, dbVideo.VideoUrl, dbVideo.ThumbnailImageUrl, dbVideo.Title, &dbVideo.Description.String, []string{}, int(dbVideo.WatchCount), dbVideo.IsPrivate, dbVideo.IsAdult, dbVideo.IsExternalCutout, dbVideo.IsAd, dbVideo.UploaderID, dbVideo.CreatedAt, dbVideo.UpdatedAt)
	for _, tag := range tags {
		video.Tags = append(video.Tags, tag.TagName)
	}

	return video, nil
}

func (i *Infrastructure) InsertVideo(ctx context.Context, id string, videoURL string, thumbnailImageURL string, title string, description *string, uploaderID string, tags []string, isAdult bool, isPrivate bool, isExternalCutout bool, isAd bool) (*domain.UploadVideoResponse, error) {
	_, err := i.db.Database.CreateVideo(ctx, sqlc.CreateVideoParams{
		ID:                id,
		VideoUrl:          videoURL,
		ThumbnailImageUrl: thumbnailImageURL,
		Title:             title,
		Description: sql.NullString{
			String: *description,
			Valid:  description != nil,
		},
		UploaderID:       uploaderID,
		IsPrivate:        isPrivate,
		IsAdult:          isAdult,
		IsExternalCutout: isExternalCutout,
		IsAd:             isAd,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
		WatchCount:       0,
	})
	if err != nil {
		return nil, err
	}

	for _, tag := range tags {
		tagInsertResult, err := i.db.Database.CreateTags(ctx, tag)
		if err != nil {
			return nil, err
		}

		tagID, err := tagInsertResult.LastInsertId()
		if err != nil {
			return nil, err
		}

		_, err = i.db.Database.CreateVideoTags(ctx, sqlc.CreateVideoTagsParams{
			VideoID: id,
			TagID:   int32(tagID),
		})
		if err != nil {
			return nil, err
		}
	}

	return &domain.UploadVideoResponse{
		ID:                id,
		VideoURL:          videoURL,
		ThumbnailImageURL: thumbnailImageURL,
		Title:             title,
		Description:       description,
		UploaderID:        uploaderID,
		Tags:              tags,
		IsAdult:           isAdult,
		IsPrivate:         isPrivate,
		IsExternalCutout:  isExternalCutout,
		IsAd:              isAd,
		// CreatedAt:         time.Now(),
	}, nil
}

func (i *Infrastructure) GetWatchCount(ctx context.Context, videoID string) (int, error) {
	var watchCountJson WatchCountJsonType
	hit, err := getFromRedis(ctx, i.redis, "watchcount"+domain.IDSeparator+videoID, &watchCountJson)
	if err != nil {
		return 0, err
	} else if hit {
		return watchCountJson.Count, nil
	}

	watchCount, err := i.db.Database.GetWatchCount(ctx, videoID)
	if err != nil {
		return 0, err
	}

	err = setToRedis(ctx, i.redis, "watchcount"+domain.IDSeparator+videoID, 1*time.Hour, &WatchCountJsonType{
		Count: int(watchCount),
	})
	if err != nil {
		return 0, err
	}

	return int(watchCount), nil
}

func (i *Infrastructure) IncrementWatchCount(ctx context.Context, videoID, userID string) (int, error) {
	_, err := i.db.Database.IncrementWatchCount(ctx, videoID)
	if err != nil {
		return 0, err
	}

	watchCount, err := i.db.Database.GetWatchCount(ctx, videoID)
	if err != nil {
		return 0, err
	}

	watchCountJsonType := WatchCountJsonType{
		Count: int(watchCount),
	}

	err = setToRedis(ctx, i.redis, videoID+domain.IDSeparator+userID, 24*time.Hour, &watchCountJsonType)
	if err != nil {
		return 0, err
	}

	return int(watchCount), nil
}

func (i *Infrastructure) ChechWatchCount(ctx context.Context, videoID, userID string) (bool, error) {
	key := videoID + domain.IDSeparator + userID

	var watchCountJson WatchCountJsonType
	hit, err := getFromRedis(ctx, i.redis, key, &watchCountJson)
	if err != nil {
		return false, err
	}
	if hit {
		return false, nil
	}
	return true, nil
}

func (i *Infrastructure) CutVideo(ctx context.Context, videoID, userID string, start, end int) (string, error) {
	const bucketName = "video"
	err := os.MkdirAll("cut-video", 0755)
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	key := videoID + domain.IDSeparator + domain.NewUUID() + ".mp4"
	outPath := "cut-video" + "/" + key
	url := fmt.Sprintf("%s/%s/output_%s.m3u8", os.Getenv("AWS_S3_URL"), bucketName, videoID)

	cmd := exec.Command("ffmpeg", "-ss", fmt.Sprintf("%d", start), "-i", url, "-to", fmt.Sprintf("%d", end-start), "-c", "copy", outPath)

	log.Println(cmd.Args)
	result, err := cmd.CombinedOutput()
	log.Println(string(result))
	if err != nil {
		return "", fmt.Errorf("failed to execute ffmpeg command: %w", err)
	}

	uploadbucketName := "cut-video"
	err = uploadVideoForS3(outPath, uploadbucketName)
	if err != nil {
		return "", err
	}

	cutURL := fmt.Sprintf("%s/%s/%s", os.Getenv("AWS_S3_URL"), uploadbucketName, key)
	return cutURL, nil
}

func (i *Infrastructure) ValidationVideo(video io.ReadSeeker) error {
	if video == nil {
		return fmt.Errorf("video is nil")
	}

	// MP4ファイルのシグネチャとして 'ftyp' を確認
	const ftyp = "ftyp"

	// 先頭の12バイトだけ読み込む（ftypボックスの確認に十分な範囲）
	header := make([]byte, 12)
	_, err := video.Read(header)
	if err != nil {
		return err
	}

	// ReadSeekerを先頭に戻す
	_, err = video.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}

	// ヘッダの4バイト目から'ftyp'が存在するかチェック
	if bytes.Contains(header[4:], []byte(ftyp)) {
		return nil
	}

	return fmt.Errorf("invalid video file")
}

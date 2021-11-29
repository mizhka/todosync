package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Retrieve a token, saves the token, then returns the generated client.
func getClient(config *oauth2.Config) *http.Client {
	// The file token.json stores the user's access and refresh tokens, and is
	// created automatically when the authorization flow completes for the first
	// time.
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

// Request a token from the web, then returns the retrieved token.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web %v", err)
	}
	return tok
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func main() {
	ctx := context.Background()
	b, err := ioutil.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	// If modifying these scopes, delete your previously saved token.json.
	config, err := google.ConfigFromJSON(b, drive.DriveScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}
	client := getClient(config)

	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Drive client: %v", err)
	}

	ticker := time.NewTicker(5 * time.Second)
	quit := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				cycle(srv, "/home/mizhka/repo/fbsd/todorepo", "/home/mizhka/notes/todos")
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()

	<-quit

	return
}

func cycle(srv *drive.Service, repo, localdir string) {

	var files = []string{"todo.txt", "done.txt"}
	var changes []string
	var commit_required bool = false

	// Google to git
	r, err := srv.Files.List().OrderBy("name").
		Q("name = 'todo.txt' or name = 'done.txt'").Fields("nextPageToken, files(id, name)").Do()
	if err != nil {
		log.Fatalf("Unable to retrieve files: %v", err)
	}
	if len(r.Files) == 0 {
		log.Fatal("No files found in google drive")
	}

	for _, i := range r.Files {
		resp, err := srv.Files.Get(i.Id).Fields("md5Checksum", "size", "version").Do()
		if err != nil {
			log.Fatalf("Unable to download file: %s %v", i.Name, err)
		}

		filename := filepath.Join(repo, i.Name)
		if resp.Md5Checksum == filemd5(filename) {
			log.Println("skip, no gdrive update:", i.Name, i.Id)
			continue
		}

		log.Printf("md5=%s vers=%d size=%d", resp.Md5Checksum, resp.Version, resp.Size)
		data, err := srv.Files.Get(i.Id).Download()
		defer data.Body.Close()

		saveFile(filename, data)
		commit_required = true
		changes = append(changes, filename)
	}
	if commit_required {
		commitToGit(repo, changes, "Push from mobile")
		for _, filename := range changes {
			pushToLocal(repo, localdir, filepath.Base(filename))
		}
		commit_required = false
		changes = []string{}
	}

	// Local to git
	for _, filename := range files {
		fullpath := filepath.Join(localdir, filename)
		localmd5 := filemd5(fullpath)
		repofile := filepath.Join(repo, filename)
		repomd5 := filemd5(repofile)
		if localmd5 == repomd5 {
			log.Println("skip, no local update:", filename)
			continue
		} else {
			log.Println("Changed local file:", filename)
		}

		pushToLocal(localdir, repo, filename)
		changes = append(changes, repofile)
		commit_required = true
	}
	if commit_required {
		commitToGit(repo, changes, "Push from local")
		for _, filename := range changes {
			pushToDrive(srv, repo, filepath.Base(filename))
		}
	}
}

func saveFile(filename string, data *http.Response) {
	out, err := os.Create(filename)
	if err != nil {
		log.Fatalf("Can't create file %s", filename)
	}
	defer out.Close()
	io.Copy(out, data.Body)
}

func pushToLocal(from, to, filename string) {
	//Read all the contents of the  original file
	bytesRead, err := ioutil.ReadFile(filepath.Join(from, filename))
	if err != nil {
		log.Fatal(err)
	}

	//Copy all the contents to the desitination file
	err = ioutil.WriteFile(filepath.Join(to, filename), bytesRead, 0644)
	if err != nil {
		log.Fatal(err)
	}
}

func filemd5(filename string) string {
	f, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		log.Fatalf("Can't open file %s: %e", filename, err.Error())
	}
	hash := md5.Sum(f)
	return hex.EncodeToString(hash[:])
}

func commitToGit(repo string, changes []string, msg string) {

	if len(changes) == 0 {
		log.Println("Nothing to commit")
		return
	}

	r, err := git.PlainOpen(repo)

	if err != nil {
		log.Fatalf("Can't open repo %s: %s", repo, err.Error())
	}

	wt, err := r.Worktree()
	if err != nil {
		log.Fatalf("Can't open worktree %s: %s", repo, err.Error())
	}

	for _, filename := range changes {
		hash, err := wt.Add(filepath.Base(filename))
		if err != nil {
			log.Fatalf("Can't add file to git %s: %s", filename, err.Error())
		} else {
			log.Println("Added file", filename, "with hash", hash.String())
		}
	}

	hash, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "ToDo Sync",
			Email: "todosync@unclebear.ru",
			When:  time.Now(),
		}})
	if err != nil {
		log.Fatalf("Can't commit to git: %s", err.Error())
	} else {
		log.Println("Committed with hash:", hash.String())
	}
}

func pushToDrive(srv *drive.Service, repo, filename string) {
	r, err := srv.Files.List().OrderBy("name").
		Q("name = '" + filename + "'").Fields("nextPageToken, files(id, name)").Do()

	if err != nil {
		log.Fatalf("Unable to retrieve files: %v", err)
	}

	gfile := r.Files[0]

	f, err := os.Open(filepath.Join(repo, filename))

	if err != nil {
		log.Fatal("Can't open file", filename)
	}
	defer f.Close()

	var updatedgFile drive.File

	updatedgFile.Name = gfile.Name
	updatedgFile.Parents = gfile.Parents
	updatedgFile.Description = gfile.Description

	_, err = srv.Files.Update(gfile.Id, &updatedgFile).Media(f, googleapi.ContentType("text/plain")).Fields("appProperties,modifiedTime,name,id").Do()
	if err != nil {
		log.Fatal("Can't upload file: ", err.Error(), " ", gfile.Id, " ", filename)
	}

}

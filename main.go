package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/viper"
)

type Config struct {
	Discord Discord `mapstructure:"discord"`
	Db      Db      `mapstructure:"db"`
}

type Db struct {
	Name     string `mapstructure:"name"`
	SyncTime int    `mapstructure:"sync_time"`
}

type Discord struct {
	Token     string `mapstructure:"token"`
	ChannelID string `mapstructure:"channel_id"`
	GuildID   string `mapstructure:"guild_id"`
}

type User struct {
	ID     int64
	UserId string
	RoleId string
	Expire time.Time
}

var config Config

var TIME_MULTIPLIER time.Duration = time.Hour

func main() {

	// Viper yaml config reader
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		log.Fatalf("Error reading config file : %v\n", err)
	}
	err = viper.Unmarshal(&config)
	if err != nil {
		log.Fatalf("Unable to unmarshal config : %v\n", err)
	}

	// Initialize database
	db, err := initDB(config.Db.Name)
	if err != nil {
		log.Fatalf("Database initialization failed : %v\n", err)
	}
	defer db.Close()

	// Make new discord function session
	session, _ := discordgo.New("Bot " + config.Discord.Token)

	// Monitor Expired User in background
	go monitorExpiredUserRoles(db, session, config)

	// Add discord slash commands
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "unrole",
			Description: "Expire and unrole user",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "user",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "role",
					Description: "role",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "time",
					Description: "time in hour",
					Required:    true,
				},
			},
		},

		{
			Name:        "unrole-check",
			Description: "Check unrole user",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "user",
					Required:    true,
				},
			},
		},
	}
	commandHandlers := map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"unrole": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			options := i.ApplicationCommandData().Options
			optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))

			for _, opt := range options {
				optionMap[opt.Name] = opt
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					// Content: fmt.Sprintf("%s assign to %s for %d hours",
					// 	optionMap["role"].RoleValue(s, config.Discord.GuildID).Name,
					// 	optionMap["user"].UserValue(s).Username,
					// 	optionMap["time"].IntValue()),
					Content: fmt.Sprintf("Assign USER :<@%s> | ROLE :<@&%s> | Expire : %d hours", optionMap["user"].UserValue(s).ID, optionMap["role"].RoleValue(s, config.Discord.GuildID).ID, optionMap["time"].IntValue()),
				},
			})

			err := s.GuildMemberRoleAdd(config.Discord.GuildID, optionMap["user"].UserValue(s).ID, optionMap["role"].RoleValue(s, config.Discord.GuildID).ID)
			if err != nil {
				log.Printf("Role assign error : %v\n", err)
				s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
					Content: fmt.Sprintf("Role assign error : %v", err),
				})
			} else {
				err = createUserRoleExpire(db, optionMap["user"].UserValue(s).ID, optionMap["role"].RoleValue(s, config.Discord.GuildID).ID, int(optionMap["time"].IntValue()))
				if err != nil {
					log.Printf("Role expire error : %v\n", err)
					s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
						Content: fmt.Sprintf("Role expire error : %v", err),
					})
				}
			}
		},
		"unrole-check": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			options := i.ApplicationCommandData().Options
			optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))

			for _, opt := range options {
				optionMap[opt.Name] = opt
			}
			var content string
			expires, err := getUserRoleExpireInfo(db, optionMap["user"].UserValue(s).ID)
			if err != nil {
				log.Printf("Get User-Role Expire Info error : %v\n", err)
				content = fmt.Sprintf("Get User-Role Expire Info error : %v", err)
			} else {
				content = fmt.Sprintf("For User %s \nRole : Time left\n", optionMap["user"].UserValue(s).Username)
				for _, expire := range expires {
					roleName, err := getRoleNamebyID(s, config.Discord.GuildID, expire.RoleId)
					if err != nil {
						log.Printf("Get User-Role Role Name error : %v\n", err)
						content = fmt.Sprintf("Get User-Role Role Name error : %v", err)
					}
					content = fmt.Sprintf("%s%s : %d hours\n", content, roleName, int(time.Until(expire.Expire).Hours()))
				}
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: content,
				},
			})

		},
	}

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
			h(s, i)
		}
	})

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v\n", s.State.User.Username, s.State.User.Discriminator)
	})

	err = session.Open()
	if err != nil {
		log.Fatalf("Cannot open the session: %v\n", err)
	}

	log.Println("Adding commands...")
	registeredCommands := make([]*discordgo.ApplicationCommand, len(commands))
	for i, v := range commands {
		cmd, err := session.ApplicationCommandCreate(session.State.User.ID, config.Discord.GuildID, v)
		if err != nil {
			log.Fatalf("Cannot create '%v' command: %v\n", v.Name, err)
		}
		registeredCommands[i] = cmd
	}

	defer session.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	log.Println("Press Ctrl+C to exit")
	<-stop

	log.Println("Removing commands...")

	registeredCommands, err = session.ApplicationCommands(session.State.User.ID, config.Discord.GuildID)
	if err != nil {
		log.Fatalf("Could not fetch registered commands: %v", err)
	}

	for _, v := range registeredCommands {
		err := session.ApplicationCommandDelete(session.State.User.ID, config.Discord.GuildID, v.ID)
		if err != nil {
			log.Fatalf("Cannot delete '%v' command: %v", v.Name, err)
		}
	}

	log.Println("Gracefully shutting down.")
}

// New function to monitor expired users
func monitorExpiredUserRoles(db *sql.DB, session *discordgo.Session, config Config) {
	ticker := time.NewTicker(time.Duration(config.Db.SyncTime) * time.Second)
	defer ticker.Stop()

	// Keep track of already notified expirations
	notifiedExpirations := make(map[int64]bool)

	for range ticker.C {
		// Check for expired users
		expiredUsers, err := getExpiredUsers(db)
		if err != nil {
			log.Printf("Error checking expired users: %v\n", err)
			continue
		}

		// Notify about newly expired users
		for _, user := range expiredUsers {
			if !notifiedExpirations[user.ID] {
				notifiedExpirations[user.ID] = true
				err = deleteUserRoleExpire(session, db, user.UserId, user.RoleId)
				if err != nil {
					log.Printf("Delete user-role error : %v\n", err)
					session.ChannelMessageSend(config.Discord.ChannelID, fmt.Sprintf("User ID : %s\n Role ID : %s \n, Error : %v", user.UserId, user.RoleId, err))
				}
			}
		}
	}
}

func initDB(db_name string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", db_name)
	if err != nil {
		return nil, err
	}

	// Create table if not exists
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		userid TEXT NOT NULL,
		roleid TEXT NOT NULL,
		expire DATETIME NOT NULL
	);
	CREATE UNIQUE INDEX IF NOT EXISTS user_idx ON users(userid, roleid);
	`)
	if err != nil {
		return nil, err
	}
	return db, err
}

func createUserRoleExpire(db *sql.DB, userId string, roleId string, expire int) error {
	query := `
		INSERT OR REPLACE INTO users (userid, roleid, expire)
		VALUES (?, ?, ?)`

	_, err := db.Exec(query, userId, roleId, time.Now().Add(TIME_MULTIPLIER*time.Duration(expire)))
	if err != nil {
		return err
	}
	return nil
}

func deleteUserRoleExpire(session *discordgo.Session, db *sql.DB, userId string, roleId string) error {

	err := session.GuildMemberRoleRemove(config.Discord.GuildID, userId, roleId)
	if err != nil {
		return err
	} else {
		session.ChannelMessageSend(config.Discord.ChannelID, "🔴Removed USER :<@"+userId+"> | ROLE :<@&"+roleId+">")
	}
	_, err = db.Exec(`DELETE FROM users WHERE userid = ? AND roleid = ?`, userId, roleId)
	if err != nil {
		return err
	}
	return nil
}

// New function to get expired users
func getExpiredUsers(db *sql.DB) ([]User, error) {
	var users []User
	query := `
	SELECT id, userid, roleid, expire
	FROM users
	WHERE expire <= datetime('now')
	ORDER BY expire`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user User
		err := rows.Scan(&user.ID, &user.UserId, &user.RoleId, &user.Expire)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

func getUserRoleExpireInfo(db *sql.DB, userId string) ([]User, error) {

	var users []User
	rows, err := db.Query(`SELECT id, userid, roleid, expire FROM users WHERE userid = ?`, userId)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user User
		err := rows.Scan(&user.ID, &user.UserId, &user.RoleId, &user.Expire)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, nil
}

func getRoleNamebyID(session *discordgo.Session, guildId string, roleId string) (string, error) {
	roles, err := session.GuildRoles(guildId)
	if err != nil {
		return "", err
	}
	for _, role := range roles {
		if role.ID == roleId {
			return role.Name, nil
		}
	}
	return "", nil
}

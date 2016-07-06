# amigo

`amigo` is a Slack bot I wrote to coordinate a capture the flag (CTF) event.

I initially forked https://github.com/rapidloop/mybot.

# setup

* get a Slack API token
* setup a mysql database:

      create table teams (id int not null auto_increment primary key, name varchar(255) not null);
      create table users (user varchar(50) primary key, team int);
      create table logs (id int not null auto_increment primary key, user varchar(50), event varchar(255), ts datetime default now());

      you will have to manually populate the users table.

* `cp config.json.sample config.json` and fill it out.

# interaction

* @amigo_bot start <team name>
  - looks up the user in the users table, gives a name to their team.
  - records log entry
  - PMs a reply with a link to the first puzzle
  - posts event to public channel
* @amigo_bot validate <flag>
  - records log entry
  - PMs a reply with yes/no
  - posts event to public channel

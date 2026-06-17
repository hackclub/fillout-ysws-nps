module github.com/hackclub/fillout-ysws-nps

go 1.26.4

replace github.com/hackclub/fillout-ysws-nps/airtable => ./airtable

replace github.com/hackclub/fillout-ysws-nps/fillout => ./fillout

require (
	github.com/hackclub/fillout-ysws-nps/airtable v0.0.0-00010101000000-000000000000
	github.com/hackclub/fillout-ysws-nps/fillout v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

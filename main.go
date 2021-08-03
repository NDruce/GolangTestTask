package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"regexp"

	"github.com/gocolly/colly/v2"
	"github.com/jasonwinn/geocoder"
	"github.com/microcosm-cc/bluemonday"
)



const (
	MAPQUEST_API_KEY = "Ключ API здесь. Получить: mapquestapi.com"
)



type geoLoc struct {
	Lat float64
	Lng float64
}

type org struct {
	Name string
	Borough string
	Address string
	Phone string
	URL string
	Geo *geoLoc
	Staff []*staff
}

type staff struct {
	Name string
	Position string
	Email string
	Phone string
}

func handler(w http.ResponseWriter, r *http.Request) {
	c := colly.NewCollector()
	orgs := []*org{}
	var StaffChan = make(chan []*staff)
	var GeoLocChan = make(chan *geoLoc)
	geocoder.SetAPIKey(MAPQUEST_API_KEY)

	c.OnError(func(r *colly.Response, err error) {
		log.Println("Ошибка загрузки страницы списка компаний", r.StatusCode, err)
	})

	c.OnHTML(".location-list-item", func(card *colly.HTMLElement) {
		info := &org{}
		info.Name = card.ChildText(".location-item--title")
		info.Borough = card.ChildText(".field-borough")
		info.Address = strings.ReplaceAll(card.ChildText(".field-location-direction"), "  ", ", ")
		info.Phone = card.ChildText(".field-location-phone a")
		info.URL = card.Request.AbsoluteURL(card.ChildAttr(".branch-view-button a", "href"))

		go locatePlace(GeoLocChan, info.Borough)
		info.Geo = <- GeoLocChan

		go getStaff(StaffChan, info.URL, info.Name)
		info.Staff = <- StaffChan
		orgs = append(orgs, info)
	})

	c.OnScraped(func(r *colly.Response) {
		log.Println("Список компаний спарсен")
	})

	c.Visit("https://ymcanyc.org/locations?type&amenities")


	b, err := json.MarshalIndent(orgs, "", "	")
	if err != nil {
		log.Println("Ошибка кодирования ответа в JSON:", err)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	w.Write(b)
}

func locatePlace(ch chan *geoLoc, Place string) {
	lat, lng, err := geocoder.Geocode(Place)
	if err == nil {
		ch <- &geoLoc{ lat, lng }
	} else {
		ch <- nil
	}
}

func getStaff(ch chan []*staff, URL string, Name string) {
	c := colly.NewCollector()
	staffList := []*staff{}
	URL += "/about"

	c.OnRequest(func(r *colly.Request) {
		r.Ctx.Put("org.Name", Name)
	})

	c.OnError(func(r *colly.Response, err error) {
		log.Println("Ошибка загрузки страницы сотрудников компании", r.StatusCode, err)
	})

	c.OnHTML(".block-description--text", func(contactsBlock *colly.HTMLElement) {
		if contactsBlock.ChildText(".field-sb-title") == "Leadership Staff" {
			portrait := &staff{}
			contactsBlock.ForEach(".field-sb-body > p", func(_ int, position *colly.HTMLElement) {
				if strings.TrimSpace(position.Text) != "" {
					HTML, err := position.DOM.Html()
					if err == nil {
						regex, _ := regexp.Compile("\\<br(.*?)\\>")
						lines := regex.Split(HTML, -1)
						if len(lines) > 1 {
							// Если сайт нормально отдал информацию
							info := &staff{}
							for _, rawLine := range lines {
								//info.Name = strings.TrimSpace(position.ChildText("strong")) // Багует
								rawLine = strings.TrimSpace(rawLine)
								stripTags := bluemonday.StripTagsPolicy()
								line := strings.TrimSpace(stripTags.Sanitize(rawLine))
								if line != "" {
									// У сайта местами кривая верстка, поэтому не удивляйтесь тому, как я тут все написал :)
									// Пришлось учитывать сбои парсинга и писать workaround'ы.
									// Надо учитывать то, что может не быть либо ФИ, либо должности, и правильно определить, что чем является

									// Если имя еще пустует, попробуем найти его в текущей строке информации о сотруднике
									if info.Name == "" {
										// Здесь подход с RegEx'ами, потому что при нормальном получении ФИ сотрудника 
										// через ChildText от Colly, из-за двойных <strong> в коде сайта, ФИ сотрудника дублируются
										regex, _ = regexp.Compile("(strong|b)\\>(.*?)\\<\\/(strong|b)")
										if regex.MatchString(rawLine) {
											info.Name = line
										}
									}

									// Если ФИ не соответствует текущей строке (и при этом не пустой), 
									// значит текущая строка является либо должностью, либо контактами. 
									// Так мы не даем ФИ занять поле должности
									if info.Name == "" || info.Name != line {
										if info.Email == "" && strings.Contains(line, "@") {
											info.Email = line
										} else {
											regex, _ = regexp.Compile("^[\\+\\d\\-\\(\\)\\s]+$")
											if info.Phone == "" && regex.MatchString(line) {
												info.Phone = line
											} else if info.Position == "" {
												info.Position = line
											}
										}
									}
								}
							}

							staffList = append(staffList, info)
						} else if position.ChildText("u") == "" {
							// Собираем портрет сотрудника при баганой верстке
							rawLine := strings.TrimSpace(HTML)
							stripTags := bluemonday.StripTagsPolicy()
							line := strings.TrimSpace(stripTags.Sanitize(HTML))

							foundName := ""
							regex, _ = regexp.Compile("(strong|b)\\>(.*?)\\<\\/(strong|b)")
							if regex.MatchString(rawLine) {
								foundName = line
							}
							if foundName != "" { // Если мы нашли именно имя
								if portrait.Name != "" {
									// Если мы нашли имя уже следующего сотрудника, то сохраняем предыдущего и создаем нового
									staffList = append(staffList, portrait)
									portrait = &staff{}
								}

								portrait.Name = foundName
							}

							if portrait.Name == "" || portrait.Name != line {
								if portrait.Email == "" && strings.Contains(line, "@") {
									portrait.Email = line
								} else {
									regex, _ = regexp.Compile("^[\\+\\d\\-\\(\\)\\s]+$")
									if portrait.Phone == "" && regex.MatchString(line) {
										portrait.Phone = line
									} else if portrait.Position == "" {
										portrait.Position = line
									}
								}
							}
						}
					}
				}
			})
		}
	})

	c.OnScraped(func(r *colly.Response) {
		log.Println("Спарсены сотрудники компании", r.Ctx.Get("org.Name"))
	})

	c.Visit(URL)
	ch <- staffList
}

func main() {
	addr := ":8181"

	http.HandleFunc("/", handler)

	log.Println("Слушаем", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
package cli

// analyticsGroup is a parent command holding related endpoints, such as the
// daily and monthly usage reports.
type analyticsGroup struct {
	use, short string
	specs      []analyticsSpec
}

// analyticsSpecs are the endpoints reached as `pcenter analytics <use>`.
// Field lists follow Microsoft's Store analytics API reference; each was also
// checked against FeedFlow's live data unless its doc comment says otherwise.
func analyticsSpecs() []analyticsSpec {
	return []analyticsSpec{
		{
			use:   "acquisitions",
			short: "List app acquisitions per day, week or month",
			long: "List app acquisitions (downloads) aggregated per day, week or month.\n\n" +
				"Every page is fetched, so the rows add up to the full total for the range.\n" +
				"Partner Center analytics lag by a day or two, so the most recent days may be missing.",
			endpoint:       "appacquisitions",
			dimensions:     []string{"osVersion", "market", "deviceType", "acquisitionType", "storeClient", "ageGroup", "gender", "orderName"},
			defaultGroupBy: "date",
			metrics:        []string{"acquisitionQuantity", "purchasePriceUSDAmount", "purchaseTaxUSDAmount"},
			aggregations:   []string{"day", "week", "month"},
			orderBy:        "date",
		},
		{
			use:   "installs",
			short: "List successful app installs per day, week or month",
			long: "List successful installs. An install is counted each time a package is installed,\n" +
				"so reinstalls and installs on a second device make this higher than acquisitions.",
			endpoint:       "installs",
			dimensions:     []string{"osVersion", "market", "deviceType", "packageVersion"},
			defaultGroupBy: "date",
			metrics:        []string{"successfulInstallCount"},
			aggregations:   []string{"day", "week", "month"},
			orderBy:        "date",
		},
		{
			use:            "ratings",
			short:          "List star ratings per day, week or month",
			long:           "List the number of one- to five-star ratings submitted in each period.",
			endpoint:       "ratings",
			dimensions:     []string{"market", "osVersion", "deviceType", "isRevised"},
			defaultGroupBy: "date",
			metrics:        []string{"oneStar", "twoStars", "threeStars", "fourStars", "fiveStars"},
			// The live endpoint answers aggregationLevel=month with daily
			// rows (checked 2026-11-12), so month is not offered.
			aggregations: []string{"day", "week"},
			orderBy:      "date",
		},
		{
			use:   "funnel",
			short: "Show the acquisition funnel: page views, acquisitions, installs, usage",
			long: "Show how many users reached each step from the Store page to using the app,\n" +
				"for the whole date range. Filter by market, deviceType, ageGroup, gender or campaignId.",
			endpoint: "funnel",
			dateless: true,
			noPaging: true,
			metrics:  []string{"MetricType", "UserCount"},
			// The funnel is the one endpoint with capitalised field names.
			rename: map[string]string{"MetricType": "metricType", "UserCount": "userCount"},
			// The live API spells the acquisition step "Acqusition"
			// (checked 2026-11-12); the docs spell it "Acquisition".
			fix: func(field, value string) string {
				if field == "metricType" && value == "Acqusition" {
					return "Acquisition"
				}
				return value
			},
		},
		{
			use:   "conversions",
			short: "List Store page clicks and conversions by channel",
			long: "List clicks on the app's Store listing and the conversions (new licences) they led to,\n" +
				"by channel: Store traffic, custom promotion campaigns, or other.",
			endpoint:       "appchannelconversions",
			dimensions:     []string{"channelType", "customCampaignId", "referrerUriDomain", "market", "deviceType", "storeClient", "appType"},
			defaultGroupBy: "date",
			metrics:        []string{"clickCount", "conversionCount"},
			// No aggregations: the live endpoint returns daily rows whatever
			// aggregationLevel says (checked 2026-11-12).
			orderBy: "date",
		},
	}
}

func analyticsGroups() []analyticsGroup {
	usageDimensions := []string{"market", "deviceType", "packageVersion", "subscriptionName"}
	return []analyticsGroup{
		{
			use:   "usage",
			short: "Read app usage: active users, sessions and engagement",
			specs: []analyticsSpec{
				{
					use:   "daily",
					short: "List daily usage: active users and devices, new users, sessions, minutes",
					long: "List daily usage, the numbers behind Partner Center's Usage report.\n\n" +
						"monthlyActiveUsers on a daily row is the rolling count for the 30 days to that date.",
					endpoint:       "usagedaily",
					dimensions:     usageDimensions,
					defaultGroupBy: "date",
					metrics: []string{"dailyActiveUsers", "dailyActiveDevices", "dailyNewUsers", "dailySessionCount",
						"engagementDurationMinutes", "monthlyActiveUsers", "monthlyActiveDevices", "monthlyNewUsers",
						"dailyNewDevices", "monthlyNewDevices"},
					orderBy: "date",
					window:  "the last 90 days",
				},
				{
					use:            "monthly",
					short:          "List monthly usage: active users and devices, new users, sessions, minutes",
					long:           "List monthly usage, one row per calendar month, dated by its first day.",
					endpoint:       "usagemonthly",
					dimensions:     usageDimensions,
					defaultGroupBy: "date",
					metrics: []string{"monthlyActiveUsers", "monthlyActiveDevices", "monthlyNewUsers", "monthlySessionCount",
						"engagementDurationMinutes", "averageDailyActiveUsers", "averageDailyActiveDevices", "monthlyNewDevices"},
					orderBy: "date",
					window:  "the last 90 days",
				},
			},
		},
	}
}

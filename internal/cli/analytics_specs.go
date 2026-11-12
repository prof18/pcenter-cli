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

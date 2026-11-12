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
			short:          "List star ratings per day or week",
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
			use:   "insights",
			short: "List notable changes Partner Center detected in acquisitions, health or usage",
			long: "List Partner Center's insights: week-over-week changes it flagged as significant in\n" +
				"acquisitions, health (failures) or usage, with the dimension that moved and by how much.\n" +
				"Rows are nested and passed through as the API returns them.",
			endpoint:    "insights",
			dateless:    true,
			passthrough: true,
			noPaging:    true,
			dataTypes:   []string{"acquisition", "health", "usage"},
			window:      "the last 30 days",
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
	addOnParam := analyticsParam{flag: "addon-id", query: "inAppProductId", usage: "Store ID of one add-on (default: all of the app's add-ons)"}
	return []analyticsGroup{
		{
			use:   "addons",
			short: "Read add-on (in-app product) acquisitions, subscriptions and conversions",
			// The add-on endpoints were checked live only for an app without
			// add-ons (empty result, 2026-11-12); field lists follow the docs.
			specs: []analyticsSpec{
				{
					use:            "acquisitions",
					short:          "List add-on acquisitions per day, week or month",
					endpoint:       "inappacquisitions",
					dimensions:     []string{"inAppProductName", "acquisitionType", "market", "osVersion", "deviceType", "storeClient", "ageGroup", "gender", "orderName"},
					defaultGroupBy: "date",
					// The docs name the add-on id inAppProductId; their own
					// sample response calls it addonProductId. Keep whichever
					// arrives.
					identity:     []string{"inAppProductId", "addonProductId"},
					metrics:      []string{"acquisitionQuantity", "purchasePriceUSDAmount", "purchaseTaxUSDAmount"},
					aggregations: []string{"day", "week", "month"},
					orderBy:      "date",
					params:       []analyticsParam{addOnParam},
				},
				{
					use:   "subscriptions",
					short: "List subscription add-on activity: new, renewed, active and churned",
					long: "List subscription add-on acquisitions per period: new subscriptions, renewals,\n" +
						"active counts by billing state, churn by reason, and gross sales before tax.",
					endpoint:       "subscriptions",
					dimensions:     []string{"subscriptionProductName", "skuId", "market", "deviceType"},
					defaultGroupBy: "date",
					identity:       []string{"subscriptionProductId"},
					metrics: []string{"newCount", "renewCount", "totalActiveCount", "totalChurnCount",
						"goodStandingActiveCount", "pendingGraceActiveCount", "graceActiveCount", "lockedActiveCount",
						"billingChurnCount", "nonRenewalChurnCount", "refundChurnCount", "chargebackChurnCount",
						"earlyChurnCount", "otherChurnCount", "grossSalesBeforeTax", "currencyCode"},
					aggregations: []string{"day", "week", "month"},
					orderBy:      "date",
					// The live API rejects top and skip with HTTP 400 although
					// the docs list them (checked 2026-11-12).
					noPaging: true,
					params:   []analyticsParam{{flag: "subscription-id", query: "subscriptionProductId", usage: "Store ID of one subscription add-on (default: all)"}},
				},
				{
					use:            "conversions",
					short:          "List add-on listing clicks and conversions by channel",
					endpoint:       "inappchannelconversions",
					dimensions:     []string{"inAppProductName", "channelType", "customCampaignId", "referrerUriDomain", "market", "deviceType", "storeClient", "appType"},
					defaultGroupBy: "date",
					identity:       []string{"inAppProductId"},
					metrics:        []string{"clickCount", "conversionCount"},
					// No aggregations, like app conversions: that endpoint
					// ignores aggregationLevel.
					orderBy: "date",
					params:  []analyticsParam{addOnParam},
				},
			},
		},
		{
			use:   "failures",
			short: "Read crash and hang reports: counts, details and stack traces",
			specs: []analyticsSpec{
				{
					use:   "list",
					short: "List failure counts (crashes, hangs) per hour, day, week or month",
					long: "List failure (crash, hang, memory, JavaScript error) counts, the numbers behind the\n" +
						"Failures section of Partner Center's Health report.\n\n" +
						"Group by failureHash to get the id that 'failures details' takes. Failures the Store\n" +
						"could not bucket have an empty failureHash and no details.",
					endpoint: "failurehits",
					dimensions: []string{"eventType", "failureName", "failureHash", "symbol", "packageVersion", "packageName",
						"osVersion", "osRelease", "market", "deviceType"},
					defaultGroupBy: "date",
					metrics:        []string{"eventCount", "deviceCount"},
					// The live endpoint returns daily rows for week and month
					// (checked 2026-11-12), so only day and hour are offered.
					aggregations: []string{"day", "hour"},
					// No orderby: this endpoint only orders by a groupby field,
					// and date is not one. Rows are sorted by date anyway.
					window: "the last 30 days (72 hours for --aggregation hour)",
				},
				{
					use:   "details",
					short: "List the individual reports for one failure, with device, OS and CAB ids",
					long: "List the individual reports behind one failure: date, market, device model, OS build,\n" +
						"package version, and the CAB id that 'failures stacktrace' takes.",
					endpoint:    "failuredetails",
					passthrough: true,
					orderBy:     "date",
					params:      []analyticsParam{{flag: "failure-hash", query: "failureHash", usage: "failureHash from 'failures list --group-by failureHash'", required: true}},
					window:      "the last 30 days",
				},
				{
					use:         "stacktrace",
					short:       "Show the stack trace of one failure report",
					long:        "Show the stack trace of one failure report, one row per frame, from its CAB id.",
					endpoint:    "stacktrace",
					passthrough: true,
					noDateRange: true,
					params:      []analyticsParam{{flag: "cab-id", query: "cabId", usage: "cabId from 'failures details'", required: true}},
					window:      "failures from the last 30 days",
				},
			},
		},
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
					use:   "monthly",
					short: "List monthly usage: active users and devices, new users, sessions, minutes",
					long: "List monthly usage, one row per calendar month, dated by its first day.\n" +
						"A month is only included when its first day is inside the range, so start --from on the 1st.",
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

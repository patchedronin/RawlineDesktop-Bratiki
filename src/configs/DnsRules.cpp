#include "include/configs/DnsRules.h"
#include "include/configs/generate.h"

#include <QCoreApplication>
#include <QDebug>
#include <QJsonDocument>
#include <QRegularExpression>

namespace Configs {
    namespace {
        const QStringList dnsRuleKinds{"", "domain", "full", "keyword", "regexp", "regex", "geosite", "rule-set"};

        void splitDnsRuleSite(const QString &raw, QString &kind, QString &value) {
            const QString site = raw.trimmed();
            const int colon = site.indexOf(':');
            kind = colon > 0 ? site.left(colon).toLower() : QString();
            value = colon > 0 ? site.mid(colon + 1).trimmed() : site;
        }
    }

    QString DnsRule::Problem() const {
        if (!enabled) return {};
        if (ParseDnsRuleSites(sites).IsEmpty()) return QCoreApplication::translate("DnsRules", "No sites");
        for (const auto &site : sites) {
            QString kind, value;
            splitDnsRuleSite(site, kind, value);
            if (!dnsRuleKinds.contains(kind)) return QCoreApplication::translate("DnsRules", "Unknown kind «%1:» in «%2»").arg(kind, site.trimmed());
            if ((kind == "regexp" || kind == "regex") && !QRegularExpression(value).isValid())
                return QCoreApplication::translate("DnsRules", "Invalid regular expression «%1»").arg(value);
        }
        DnsAddress address;
        QString why;
        if (!ParseDnsAddress(server, &address, &why)) return QCoreApplication::translate("DnsRules", "DNS server «%1»: %2").arg(server, why);
        return {};
    }

    QList<DnsRule> DecodeDnsRules(const QString &stored) {
        QList<DnsRule> rules;
        const auto doc = QJsonDocument::fromJson(stored.toUtf8());
        if (!doc.isArray()) return rules;
        for (const auto &entry : doc.array()) {
            if (!entry.isObject()) continue;
            const auto obj = entry.toObject();
            DnsRule rule;
            rule.enabled = obj.value("enabled").toBool(true);
            for (const auto &site : obj.value("sites").toArray())
                if (const auto text = site.toString().trimmed(); !text.isEmpty()) rule.sites << text;
            rule.server = obj.value("server").toString().trimmed();
            rule.direct = obj.value("direct").toBool(false);
            rules << rule;
        }
        return rules;
    }

    QString EncodeDnsRules(const QList<DnsRule> &rules) {
        QJsonArray array;
        for (const auto &rule : rules)
            array << QJsonObject{{"enabled", rule.enabled}, {"sites", QJsonArray::fromStringList(rule.sites)},
                                 {"server", rule.server}, {"direct", rule.direct}};
        return QString::fromUtf8(QJsonDocument(array).toJson(QJsonDocument::Compact));
    }

    QStringList DnsRuleLines(const QString &text) {
        QStringList out;
        static const QRegularExpression urlRule(QStringLiteral("\\s*rule-set:\\s*https?://"),
                                                QRegularExpression::CaseInsensitiveOption);
        for (const auto &line : text.split('\n')) {
            qsizetype start = 0;
            bool url = urlRule.match(line, 0,
                QRegularExpression::NormalMatch, QRegularExpression::AnchorAtOffsetMatchOption).hasMatch();
            for (qsizetype i = 0; i <= line.size(); ++i) {
                if (i < line.size() && (line.at(i) != ',' ||
                    (url && (i + 1 == line.size() || !line.at(i + 1).isSpace())))) continue;
                const auto site = line.mid(start, i - start).trimmed();
                if (!site.isEmpty() && !site.startsWith('#')) out << site;
                start = i + 1;
                if (start < line.size()) url = urlRule.match(line, start,
                    QRegularExpression::NormalMatch, QRegularExpression::AnchorAtOffsetMatchOption).hasMatch();
            }
        }
        return out;
    }

    DnsRuleMatch ParseDnsRuleSites(const QStringList &sites) {
        DnsRuleMatch match;
        for (const auto &site : sites) {
            QString kind, value;
            splitDnsRuleSite(site, kind, value);
            if (value.isEmpty()) continue;
            if (kind == "full") match.domains << value.toLower();
            else if (kind == "keyword") match.keywords << value;
            else if (kind == "regexp" || kind == "regex") match.regexes << value;
            else if (kind == "geosite") match.ruleSets << "geosite-" + value.toLower();
            else if (kind == "rule-set") match.ruleSets << value;
            // domain: and a bare name both take the subdomains, as v2ray reads them.
            else {
                if (value.startsWith('.')) value.remove(0, 1);
                match.suffixes << value.toLower();
            }
        }
        return match;
    }

    int TestDnsRules() {
        int failures = 0;
        const auto check = [&failures](bool ok, const char *what) {
            if (!ok) {
                qCritical() << "DNS rules self-test failed:" << what;
                ++failures;
            }
        };
        const DnsRule twitch{true, {"geosite:category-ai-chat-!cn", "domain:usher.ttvnw.net", "full:gql.twitch.tv", "keyword:openai",
                                    "regexp:^chat[0-9]+\\.example\\.org$", "example.com"}, "https://dns.zapret.moe/dns-query", false};
        const auto match = ParseDnsRuleSites(twitch.sites);
        check(match.suffixes == QJsonArray{"usher.ttvnw.net", "example.com"}, "domain: and bare names are suffixes");
        check(match.domains == QJsonArray{"gql.twitch.tv"}, "full: is one domain");
        check(match.keywords == QJsonArray{"openai"}, "keyword:");
        check(match.regexes.size() == 1, "regexp:");
        check(match.ruleSets == QStringList{"geosite-category-ai-chat-!cn"}, "geosite: is a catalog list");
        check(ParseDnsRuleSites({"rule-set:custom-list", "rule-set:https://example.org/sites.srs"}).ruleSets ==
            QStringList{"custom-list", "https://example.org/sites.srs"}, "picker entries preserve catalog names and URLs");
        check(DnsRule{true, {"rule-set:custom-list"}, "1.1.1.1", false}.Problem().isEmpty(), "selected DNS rule sets validate");
        check(DnsRuleLines("a.com\n\n# note\nb.com, c.com") == QStringList{"a.com", "b.com", "c.com"}, "lines");
        check(DnsRuleLines("rule-set:https://example.org/sites.srs?labels=a,full:b, full:local.test") ==
            QStringList{"rule-set:https://example.org/sites.srs?labels=a,full:b", "full:local.test"},
            "the editor preserves URL commas and the following exact-domain rule");
        const QList<DnsRule> rules{twitch, DnsRule{false, {"x.org"}, "1.1.1.1", true}};
        check(DecodeDnsRules(EncodeDnsRules(rules)) == rules, "round trip");
        for (const auto &bad : {QString(), QString("{"), QString("{\"a\":1}"), QString("[1,\"x\",null]")})
            check(DecodeDnsRules(bad).isEmpty(), "damaged storage reads as no rules");
        check(twitch.Problem().isEmpty(), "a valid rule");
        check(!DnsRule{true, {}, "1.1.1.1", false}.Problem().isEmpty(), "no sites");
        check(!DnsRule{true, {"regexp:(["}, "1.1.1.1", false}.Problem().isEmpty(), "bad regex");
        check(!DnsRule{true, {"ip:1.2.3.4"}, "1.1.1.1", false}.Problem().isEmpty(), "unknown kind");
        check(!DnsRule{true, {"x.org"}, "", false}.Problem().isEmpty(), "no server");
        check(DnsRule{false, {}, "", false}.Problem().isEmpty(), "disabled rules are not checked");
        qInfo() << "DNS rules self-test:" << (failures == 0 ? "passed" : "failed");
        return failures == 0 ? 0 : 1;
    }
}

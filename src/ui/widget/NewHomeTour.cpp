// --ui-tour <dir>: walks the new interface in each of its themes and saves what the screen shows, for CI.
// The whole screen is grabbed, so window corners, shadows and translucency come out as the user sees them.
#include "include/ui/widget/NewHome.hpp"

#include "include/ui/mainwindow.h"
#include "include/ui/profile/dialog_edit_profile.h"
#include "include/ui/setting/ThemeManager.hpp"
#include "include/configs/sub/GroupUpdater.hpp"
#include "include/database/SettingsRepo.h"
#include "include/engine/EngineHost.h"

#include <QApplication>
#include <QBoxLayout>
#include <QDialog>
#include <QDialogButtonBox>
#include <QDir>
#include <QEventLoop>
#include <QFile>
#include <QTextStream>
#include <QMenu>
#include <QKeyEvent>
#include <QMouseEvent>
#include <QFontMetrics>
#include "include/database/GroupsRepo.h"
#include <QPointer>
#include <QPushButton>
#include <QStyleFactory>
#include <QRegularExpression>
#include <QScreen>
#include <QTabWidget>
#include <QTimer>

#include <algorithm>
#include <functional>
#include <memory>

namespace {
    // Sample servers on documentation addresses: rows to look at, nothing to connect to.
    const char *kSampleLinks =
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.10:443?security=reality&type=tcp&sni=example.com&pbk=YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY&sid=01#%F0%9F%87%B8%F0%9F%87%AA%20se-tcp-reality\n"
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.11:443?security=tls&type=xhttp&sni=example.com#%F0%9F%87%B7%F0%9F%87%BA%20ru-xhttp-tls\n"
        "hysteria2://secret@203.0.113.12:443?sni=example.com#%F0%9F%87%A9%F0%9F%87%AA%20de-hy2\n"
        "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzZWNyZXQ@203.0.113.13:8388#nl-ss\n";

    struct Tour {
        MainWindow *mw;
        QDir dir;
        QList<std::pair<QString, std::function<void()>>> steps;
        int at = 0;
        QString theme;

        void shoot(const QString &name) {
            const QString file = dir.filePath(QStringLiteral("%1-%2-%3.png").arg(theme).arg(at, 3, 10, QLatin1Char('0')).arg(name));
            if (QScreen *screen = QGuiApplication::primaryScreen()) screen->grabWindow(0).save(file);
        }
        // Everything the step opened goes away before the next one: popups, menus and dialog windows.
        void clear() {
            for (int pass = 0; pass < 3; ++pass) {
                if (QWidget *popup = QApplication::activePopupWidget()) { popup->close(); continue; }
                bool closed = false;
                for (QWidget *w : QApplication::topLevelWidgets()) {
                    if (w == mw || !w->isVisible()) continue;
                    if (auto *dialog = qobject_cast<QDialog *>(w)) dialog->reject();
                    else w->close();
                    closed = true;
                }
                if (!closed) break;
            }
        }
    };

    void next(const std::shared_ptr<Tour> &tour);

    void run(const std::shared_ptr<Tour> &tour) {
        if (tour->at >= tour->steps.size()) {
            if (auto *host = Engine::host()) host->shutdown();
            QCoreApplication::exit(0);
            return;
        }
        const auto &[name, open] = tour->steps[tour->at];
        if (name == QLatin1String("theme-switch-regression")) {
            open();
            tour->shoot(name);
            tour->clear();
            next(tour);
            return;
        }
        // Armed first: a step that opens a modal dialog returns only once the dialog is closed.
        QTimer::singleShot(1400, qApp, [tour, name = name] {
            if (name == QLatin1String("edit-freeturn")) {
                DialogEditProfile *dialog = nullptr;
                for (auto *widget : QApplication::topLevelWidgets()) {
                    if (auto *editor = qobject_cast<DialogEditProfile *>(widget); editor && editor->isVisible()) {
                        dialog = editor;
                        break;
                    }
                }
                auto *box = dialog ? dialog->findChild<QDialogButtonBox *>() : nullptr;
                auto *ok = box ? box->button(QDialogButtonBox::Ok) : nullptr;
                if (!ok || !ok->isVisible() ||
                    !dialog->screen()->availableGeometry().contains(QRect(ok->mapToGlobal(QPoint()), ok->size())))
                    qFatal("The FreeTurn save button is outside the available screen");
            }
            if (!name.isEmpty()) tour->shoot(name);
            tour->clear();
            next(tour);
        });
        open();
    }

    void next(const std::shared_ptr<Tour> &tour) {
        ++tour->at;
        QTimer::singleShot(500, qApp, [tour] { run(tour); });
    }
}

void NewHome::runTour(MainWindow *mw, const QString &dir) {
    auto tour = std::make_shared<Tour>();
    tour->mw = mw;
    QDir().mkpath(dir);
    tour->dir = QDir(dir);
    auto &s = tour->steps;
    const auto action = [](QAction *a) { return [a] { a->trigger(); }; };
    const auto home = [mw] { return mw->newHome; };

    s << std::pair{QString(), std::function<void()>([tour] {
        tour->clear();
        Subscription::updater()->ImportText(QString::fromUtf8(kSampleLinks));
        // Local sample data only: display the subscription routing row without fetching a subscription.
        if (const auto group = Configs::dataManager->groupsRepo->CurrentGroup()) group->url = QStringLiteral("https://subscription.example.invalid");
    })};
    for (const QString theme : {QString::fromLatin1(NewStyle::OledTheme), QString::fromLatin1(NewStyle::WhiteTheme), QString::fromLatin1(NewStyle::PastelTheme)}) {
        s << std::pair{QString(), std::function<void()>([tour, mw, theme] {
            tour->theme = theme;
            Configs::dataManager->settingsRepo->theme = theme;
            Configs::dataManager->settingsRepo->new_layout_panel = QStringLiteral("left");
            themeManager()->ApplyTheme(theme);
            mw->showNormal();
            mw->resize(1000, 700);
            mw->move(20, 20);
            mw->raise();
            mw->activateWindow();
        })};
        s << std::pair{QStringLiteral("home"), std::function<void()>([home] {
            if (auto *h = home(); h && h->m_list->model() && h->m_list->model()->rowCount() > 1) h->m_list->setCurrentIndex(h->m_list->model()->index(1, 0));
        })};
        s << std::pair{QStringLiteral("empty-space-selection"), std::function<void()>([home, mw] {
            auto *h = home();
            const auto check = [](bool ok, const char *message) { if (!ok) qFatal("Selection regression: %s", message); };
            auto *selection = h->m_list->selectionModel();
            const auto row = h->m_list->model()->index(1, 0);
            check(row.isValid(), "sample profile missing");
            const int running = mw->runningSnapshot() ? mw->runningSnapshot()->id : -1;
            const auto choose = [&] { selection->select(row, QItemSelectionModel::ClearAndSelect | QItemSelectionModel::Rows); };
            const auto click = [](QWidget *widget, QPoint point) {
                const QPointF global = widget->mapToGlobal(point);
                QMouseEvent press(QEvent::MouseButtonPress, QPointF(point), global, Qt::LeftButton, Qt::LeftButton, Qt::NoModifier);
                QApplication::sendEvent(widget, &press);
                QMouseEvent release(QEvent::MouseButtonRelease, QPointF(point), global, Qt::LeftButton, Qt::NoButton, Qt::NoModifier);
                QApplication::sendEvent(widget, &release);
            };
            choose();
            click(h->m_more, h->m_more->rect().center());
            if (auto *popup = QApplication::activePopupWidget()) popup->close();
            check(selection->hasSelection(), "clicking a control cleared the selected profile");
            const QPoint blank(h->width() / 2, h->height() - 12);
            check(!h->childAt(blank), "sample background point is not empty");
            click(h, blank);
            check(!selection->hasSelection(), "empty home background kept selection");
            choose();
            const QPoint gap(1, h->m_list->visualRect(row).center().y());
            click(h->m_list->viewport(), gap);
            check(!selection->hasSelection(), "empty tile margin kept selection");
            choose();
            const QPoint below(h->m_list->viewport()->width() / 2, h->m_list->viewport()->height() - 1);
            check(!h->m_list->indexAt(below).isValid(), "sample list has no empty area");
            click(h->m_list->viewport(), below);
            check(!selection->hasSelection(), "empty list background kept selection");
            check((mw->runningSnapshot() ? mw->runningSnapshot()->id : -1) == running, "clearing view selection changed the running profile");
            check(h->m_routeBox->isVisible(), "subscription routing row missing");
            check(h->m_routeBox->findChildren<QAbstractButton *>().size() == 1, "routing icon missing");
        })};
        s << std::pair{QStringLiteral("groups"), std::function<void()>([home] { home()->showGroupPicker(); })};
        s << std::pair{QStringLiteral("hidden-subscription-block"), std::function<void()>([home] {
            auto *h = home();
            auto &settings = *Configs::dataManager->settingsRepo;
            settings.new_layout_chip = QStringLiteral("hidden");
            h->applyArrangement();
            QApplication::processEvents();
            if (h->m_chipBox->isVisible() || !h->m_add->isVisible() ||
                h->m_subscriptionTitle->indexOf(h->m_add) + 1 != h->m_subscriptionTitle->indexOf(h->m_refresh))
                qFatal("Hidden subscription controls are not reachable");
        })};
        s << std::pair{QStringLiteral("hidden-block-group-picker"), std::function<void()>([home, errorPath = tour->dir.filePath(QStringLiteral("keyboard-picker-error.txt"))] {
            auto *h = home();
            auto *heading = h->m_groupHeading;
            const auto require = [&errorPath](bool passed, const char *message) {
                if (passed) return;
                QFile file(errorPath);
                if (file.open(QIODevice::WriteOnly | QIODevice::Text)) {
                    file.write(message);
                    file.close();
                }
                qFatal("%s", message);
            };
            require(heading->focusPolicy() & Qt::TabFocus, "Hidden subscription heading cannot be reached by Tab");
            // Exercise the heading's handler independently of application shortcuts and runner window activation.
            QKeyEvent enter(QEvent::KeyPress, Qt::Key_Return, Qt::NoModifier);
            require(h->eventFilter(heading, &enter), "The subscription heading did not handle Enter");
            const auto pickers = h->findChildren<QWidget *>(QStringLiteral("newHomeGroupPicker"));
            require(std::any_of(pickers.cbegin(), pickers.cend(), [](const QWidget *picker) { return picker->isVisible(); }),
                    "Enter did not show the subscription picker");
        })};
        s << std::pair{QStringLiteral("fixed-mode-switches"), std::function<void()>([home] {
            auto *h = home();
            h->applyArrangement();
            if (!h->m_tun->isVisible() || !h->m_proxy->isVisible() ||
                h->m_switches->indexOf(h->m_tun) != 0 || h->m_switches->indexOf(h->m_proxy) != 1)
                qFatal("Both mode switches must stay visible in their fixed order");
        })};
        s << std::pair{QString(), std::function<void()>([home] {
            auto &settings = *Configs::dataManager->settingsRepo;
            settings.new_layout_chip = QStringLiteral("top");
            home()->applyArrangement();
        })};
        s << std::pair{QStringLiteral("more-menu"), std::function<void()>([home] { home()->showMoreMenu(); })};
        s << std::pair{QStringLiteral("add-menu"), std::function<void()>([home] { home()->showAddMenu(); })};
        s << std::pair{QStringLiteral("group-menu"), std::function<void()>([home] { home()->showGroupMenu(); })};
        s << std::pair{QStringLiteral("server-menu"), std::function<void()>([mw] { mw->ui->menu_server->popup(QPoint(400, 300)); })};
        s << std::pair{QStringLiteral("tray-menu"), std::function<void()>([mw] { mw->trayMenu->popup(QPoint(600, 300)); })};
        for (int tab = 1; tab <= 4; ++tab) {
            s << std::pair{QStringLiteral("tab%1").arg(tab), std::function<void()>([home, tab] { home()->selectTab(tab, false); })};
        }
        s << std::pair{QStringLiteral("arrange"), std::function<void()>([home] { home()->selectTab(0, false); home()->setArranging(true); })};
        s << std::pair{QStringLiteral("arrange-plain"), std::function<void()>([home] {
            for (auto *w : home()->findChildren<QWidget *>(QStringLiteral("arrangeTutorial"))) w->close();
        })};
        s << std::pair{QStringLiteral("narrow-center"), std::function<void()>([home, mw, dir] {
            home()->setArranging(false);
            for (const QString panel : {QStringLiteral("left"), QStringLiteral("right"), QStringLiteral("center")}) {
                Configs::dataManager->settingsRepo->new_layout_panel = panel;
                home()->applyArrangement();
                mw->resize(380, std::min(760, mw->screen()->availableGeometry().height() - 24));
                QApplication::processEvents();
                mw->applyTopBarMetrics();
                QApplication::processEvents();
                auto *h = home();
                QFile trace(QDir(dir).filePath("layout-geometry.txt"));
                if (trace.open(QIODevice::WriteOnly | QIODevice::Append)) {
                    QTextStream(&trace) << panel << " window=" << mw->width() << 'x' << mw->height()
                        << " homeHeight=" << h->height() << " minimumHeight=" << h->minimumHeight()
                        << " sideY=" << h->m_side->y() << " sideHeight=" << h->m_side->height()
                        << " bodyY=" << h->m_bodyLayout->geometry().top() << '\n';
                }
                const auto bounds = [h](QWidget *widget) { return QRect(widget->mapTo(h, QPoint()), widget->size()); };
                const QRect orb = bounds(h->m_orb), tun = bounds(h->m_tun), proxy = bounds(h->m_proxy);
                auto body = h->m_bodyLayout->geometry();
                body.moveTopLeft(h->m_side->parentWidget()->mapTo(h, body.topLeft()));
                if (bounds(h->m_side).intersects(body))
                    qFatal("Side panel overlaps the body in %s: window=%dx%d side=%d bodyTop=%d minimumHeight=%d",
                           qPrintable(panel), mw->width(), mw->height(), h->m_side->height(),
                           h->m_bodyLayout->geometry().top(), h->windowMinimum().height());
                if (mw->height() > mw->screen()->availableGeometry().height())
                    qFatal("The main window exceeds the available screen height in %s", qPrintable(panel));
                if (orb.intersects(tun) || (h->m_proxy->isVisible() && (orb.intersects(proxy) || tun.intersects(proxy))) ||
                    bounds(h->m_controls).intersects(bounds(h->m_running)) || bounds(h->m_controls).intersects(bounds(h->m_inbound)))
                    qFatal("Connect controls overlap in %s", qPrintable(panel));
                const QFontMetrics metrics(NewStyle::font(14, QFont::Medium));
                for (const auto *tab : home()->m_tabs)
                    if (tab->width() < metrics.horizontalAdvance(tab->text()) + 20)
                        qFatal("A full tab title does not fit the minimum window in %s", qPrintable(panel));
            }
        })};
        s << std::pair{QString(), std::function<void()>([home, mw] {
            Configs::dataManager->settingsRepo->new_layout_panel = QStringLiteral("left");
            home()->applyArrangement();
            mw->resize(1000, 700);
        })};
        for (const auto &[name, a] : {std::pair{"add-input", mw->ui->menu_add_from_input}, std::pair{"manage-groups", mw->ui->menu_manage_groups},
                 std::pair{"new-group", mw->ui->actionAdd_New_Group}, std::pair{"edit-group", mw->ui->actionEdit_Group},
                 std::pair{"routing", mw->ui->menu_routing_settings}, std::pair{"vpn", mw->ui->menu_vpn_settings},
                 std::pair{"vk-relay", mw->ui->menu_vk_relay_settings}, std::pair{"presets", mw->ui->menu_preset_settings},
                 std::pair{"integration", mw->ui->menu_integration_settings}, std::pair{"otp", mw->ui->menu_otp_manager},
                 std::pair{"scanner", mw->ui->menu_scanner}, std::pair{"traffic", mw->ui->actionTraffic_Stats},
                 std::pair{"about", mw->ui->actionAbout}, std::pair{"find-settings", mw->ui->menu_find_settings}}) {
            s << std::pair{QString::fromLatin1(name), std::function<void()>(action(a))};
        }
        s << std::pair{QStringLiteral("edit-profile"), std::function<void()>([home, mw] {
            if (auto *h = home(); h->m_list->model() && h->m_list->model()->rowCount() > 0) mw->on_profilesTableView_doubleClicked(h->m_list->model()->index(0, 0));
        })};
        s << std::pair{QStringLiteral("edit-freeturn"), std::function<void()>([mw] {
            auto *editor = new DialogEditProfile("freeturn", Configs::dataManager->settingsRepo->current_group, mw);
            editor->setAttribute(Qt::WA_DeleteOnClose);
        })};
        // The settings dialog page by page: it holds most of the program's fields.
        s << std::pair{QStringLiteral("settings"), std::function<void()>(action(mw->ui->menu_basic_settings))};
        for (int page = 1; page < 10; ++page) {
            s << std::pair{QStringLiteral("settings-page%1").arg(page), std::function<void()>([tour, page] {
                QTimer::singleShot(0, qApp, [tour, page] {
                    tour->mw->ui->menu_basic_settings->trigger();
                    QTimer::singleShot(600, qApp, [page] {
                        for (QWidget *w : QApplication::topLevelWidgets()) {
                            if (!w->isVisible() || !qobject_cast<QDialog *>(w)) continue;
                            if (auto *tabs = w->findChild<QTabWidget *>(); tabs && page < tabs->count()) tabs->setCurrentIndex(page);
                        }
                    });
                });
            })};
        }
        s << std::pair{QStringLiteral("theme-switch-regression"), std::function<void()>([theme, mw, tour] {
            const auto check = [](bool ok, const char *message) { if (!ok) qFatal("Theme regression: %s", message); };
            check(!NewStyle::styleSheet().contains(QRegularExpression("%[0-9]+")), "unresolved palette placeholder");
            QDialog dialog(mw);
            const auto flags = dialog.windowFlags();
            const QMargins margins(7, 9, 11, 13);
            dialog.setContentsMargins(margins);
            QPointer<QPushButton> child = new QPushButton(QStringLiteral("Regression"), &dialog);
            dialog.show();
            QApplication::processEvents();
            check(dialog.testAttribute(Qt::WA_TranslucentBackground), "new dialog must have a translucent surface");
            const QStringList classicThemes = QStringList{"System", "QDarkStyle", "FlatGray", "LightBlue", "SoftPink", "BlackSoft"}
                + QStyleFactory::keys();
            for (const auto &classic : classicThemes) {
                themeManager()->ApplyTheme(classic);
                QApplication::processEvents();
                check(child && dialog.windowFlags() == flags, "classic dialog flags or children changed");
                check(dialog.contentsMargins() == margins, "title bar space leaked into classic layout");
                check(!dialog.testAttribute(Qt::WA_TranslucentBackground) && !dialog.testAttribute(Qt::WA_NoSystemBackground), "classic dialog still transparent");
                themeManager()->ApplyTheme(theme);
                QApplication::processEvents();
                check(dialog.property("newFrame").toBool(), "existing dialog did not recover the new frame");
            }
            dialog.close();
            const auto captionButtons = Qt::WindowTitleHint | Qt::WindowMinimizeButtonHint |
                                        Qt::WindowMaximizeButtonHint | Qt::WindowCloseButtonHint;
            for (const auto &classic : classicThemes) {
                themeManager()->ApplyTheme(classic);
                QEventLoop settle;
                QTimer::singleShot(300, &settle, &QEventLoop::quit);
                settle.exec();
                const auto classicFlags = mw->windowFlags();
                check(mw->isVisible() && !(classicFlags & Qt::FramelessWindowHint), "main window did not regain its system frame");
                check((classicFlags & captionButtons) == captionButtons, "system title bar buttons are missing");
                check(mw->mask().isEmpty(), "new theme corner mask survived the switch");
                tour->shoot(QStringLiteral("theme-to-") + classic);
                themeManager()->ApplyTheme(theme);
                check(mw->windowFlags() & Qt::FramelessWindowHint, "main window did not recover the Rawline frame");
            }
        })};
    }
    QTimer::singleShot(4000, qApp, [tour] { run(tour); });
}
